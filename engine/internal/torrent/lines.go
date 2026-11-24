package torrent

import (
	"context"
	"log"
	"slices"
	"sync/atomic"
	"time"

	atorrent "github.com/anacrolix/torrent"

	"downloadmanagersquared/engine/internal/rate"
)

// lineTor is a torrent as one line carries it: the line's client holds it, as
// a peer of its own in the swarm.
type lineTor struct {
	t      *Torrent
	is     *ifaceSockets
	at     *atorrent.Torrent
	ctx    context.Context // ends when the line leaves the torrent
	cancel context.CancelFunc
	added  time.Time

	// Below, the torrent's run goroutine only.

	multi bool   // the coordinator sets its piece priorities, not the files' priorities
	prio  []bool // multi: the pieces it is told to fetch
	skip  []int  // single: the files not fetched, as last applied
	set   bool   // single: skip was applied
	rate  rate.EWMA
	avail []bool      // the pieces some peer of the line has; refreshed now and then
	lag   map[int]int // pieces done on another line that this one still lacks, and for how many ticks

	twice atomic.Int64 // useful bytes of pieces another line had received blocks of first
}

// open is whether the line's torrent is still in its client.
func (lt *lineTor) open() bool {
	select {
	case <-lt.at.Closed():
		return false
	default:
		return lt.ctx.Err() == nil
	}
}

// drop takes the torrent out of the line's client, once balance is not touching it.
func (t *Torrent) drop(lt *lineTor) {
	lt.cancel()
	t.s.lineOf.Delete(lt.at)
	t.balMu.Lock()
	lt.at.Drop()
	t.balMu.Unlock()
}

// wakeUp tells the torrent's run that the lines changed, or that something it
// applies (the chosen files) did.
func (t *Torrent) wakeUp() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// lineTors are the lines carrying the torrent now, the main one first. The
// slice is never changed in place.
func (t *Torrent) lineTors() []*lineTor {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lts
}

// main is the torrent on the main line of those carrying it, nil if none.
func (t *Torrent) main() *atorrent.Torrent {
	if lts := t.lineTors(); len(lts) > 0 {
		return lts[0].at
	}
	return nil
}

// wantLines are the lines the torrent should run on. A private torrent runs on
// the main line only: its tracker may count each peer ID or IP of an account as
// another user, and flag the account. Until the info is known (a magnet link
// fetching it) one line asks for it, and the others get it from there.
func (t *Torrent) wantLines() []*ifaceSockets {
	all := t.s.lines()
	if len(all) <= 1 {
		return all
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.st.Info == nil {
		return all[:1]
	}
	if info, err := t.infoLocked(); err != nil || info.Private != nil && *info.Private {
		return all[:1]
	}
	// Another line joins once the first has checked the data on disk: it then finds
	// every piece's completion in the shared storage and hashes nothing, and can't
	// race a piece the first line is writing.
	if len(t.lts) > 0 && checking(t.lts[0].at) && len(t.lts) == 1 {
		return all[:1]
	}
	return all
}

// syncLines makes the lines carrying the torrent the ones it should run on:
// it joins new lines and lets go of those that left. It returns the first error
// of joining a line.
func (t *Torrent) syncLines(ctx context.Context) error {
	want := t.wantLines()
	have := t.lineTors()
	var err error
	for _, lt := range have {
		if !slices.Contains(want, lt.is) {
			t.retire(lt)
		}
	}
	for _, is := range want {
		if slices.ContainsFunc(t.lineTors(), func(lt *lineTor) bool { return lt.is == is }) {
			continue
		}
		select {
		case <-is.gone:
			continue // closed since the snapshot
		default:
		}
		lt, e := t.addLine(ctx, is, is == want[0])
		if e != nil {
			log.Printf("torrent %s on %s: %v", t.hash, is.ID, e)
			if err == nil {
				err = e
			}
			continue
		}
		t.mu.Lock()
		if ctx.Err() != nil { // left the swarm meanwhile: dropLocked did not see this one
			t.mu.Unlock()
			t.drop(lt)
			return ctx.Err()
		}
		lts := append(slices.Clone(t.lts), lt)
		slices.SortStableFunc(lts, func(a, b *lineTor) int { return slices.Index(want, a.is) - slices.Index(want, b.is) })
		t.lts = lts
		t.mu.Unlock()
	}
	return err
}

// retire lets go of a line that left the torrent, keeping what it uploaded.
func (t *Torrent) retire(lt *lineTor) {
	st := lt.at.Stats()
	up, down := st.BytesWrittenData.Int64(), st.BytesReadUsefulData.Int64()
	t.mu.Lock()
	t.lts = slices.DeleteFunc(slices.Clone(t.lts), func(o *lineTor) bool { return o == lt })
	t.upDone += up
	t.downDone += down
	t.mu.Unlock()
	t.drop(lt)
}

// addLine puts the torrent in is's client. main: it is the main line, which
// also carries web seeds.
func (t *Torrent) addLine(ctx context.Context, is *ifaceSockets, main bool) (*lineTor, error) {
	spec, err := atorrent.TorrentSpecFromMagnetUri(t.st.Magnet) // fixed since Add
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	spec.InfoBytes = t.st.Info
	spec.Storage = t.shared
	t.mu.Unlock()
	if !main {
		spec.Webseeds = nil
	}
	var missed []string
	spec.Trackers, missed = resolveUDPTrackers(ctx, spec.Trackers, is.LookupIP)
	at, _, err := is.cl.AddTorrentSpec(spec)
	if err != nil {
		return nil, err
	}
	lctx, cancel := context.WithCancel(ctx)
	lt := &lineTor{t: t, is: is, at: at, ctx: lctx, cancel: cancel, added: time.Now()}
	t.s.lineOf.Store(at, lt)
	at.SetMaxEstablishedConns(t.s.maxPeers())
	if len(missed) > 0 {
		go resolveLater(lctx, missed, is.LookupIP, at.AddTrackers)
	}
	go t.announceDHT(lt)
	return lt, nil
}

const (
	aliveGrace = 15 * time.Second // a line without peers yet still counts as alive this long
	rampUp     = 20 * time.Second // a line that has fetched nothing yet is given the best line's share this long
	availEvery = 4                // ticks between reading which pieces a line's peers have
)

// balance is run each poll: it makes finished pieces known to every line and,
// while the torrent downloads, splits the pieces still to come between them.
// One line needs none of it: the files' priorities do.
func (t *Torrent) balance(lts []*lineTor, fetch bool, tick int) {
	if len(lts) == 0 {
		return
	}
	t.mu.Lock()
	skip := t.st.Skip
	t.mu.Unlock()
	// Held while pieces are touched: dropping a line's torrent meanwhile (retire, dropLocked)
	// waits, as the library panics on a piece's priority changed after its torrent closed.
	t.balMu.Lock()
	defer t.balMu.Unlock()
	for _, lt := range lts {
		if !lt.open() {
			return // its client closed under us: the next sync drops it
		}
	}
	if len(lts) == 1 {
		t.single(lts[0], skip, fetch)
		return
	}
	t.balanceMany(lts, skip, fetch, tick)
}

// single sets what one line fetches: the files chosen.
func (t *Torrent) single(lt *lineTor, skip []int, fetch bool) {
	if lt.at.Info() == nil {
		return
	}
	if lt.multi { // the other lines left: back to the files' priorities, set before the pieces' go
		if fetch {
			applySkip(lt.at, skip)
		}
		for i, on := range lt.prio {
			if on {
				lt.at.Piece(i).SetPriority(atorrent.PiecePriorityNone)
			}
		}
		lt.multi, lt.prio, lt.set = false, nil, false
	}
	if fetch && !(lt.set && slices.Equal(lt.skip, skip)) {
		applySkip(lt.at, skip)
		lt.skip, lt.set = slices.Clone(skip), true
	}
}

// pieceView is how one line sees the pieces.
type pieceView struct {
	complete, partial []bool
	checking          bool
}

func viewOf(at *atorrent.Torrent, n int) pieceView {
	v := pieceView{complete: make([]bool, n), partial: make([]bool, n)}
	i := 0
	for _, r := range at.PieceStateRuns() {
		if r.Checking {
			v.checking = true
		}
		for j := i; j < i+r.Length && j < n; j++ {
			v.complete[j], v.partial[j] = r.Complete, r.Partial
		}
		i += r.Length
	}
	return v
}

// balanceMany is balance for several lines.
func (t *Torrent) balanceMany(lts []*lineTor, skip []int, fetch bool, tick int) {
	main := lts[0].at
	if main.Info() == nil {
		return
	}
	n := main.NumPieces()
	views := make([]pieceView, len(lts))
	for li, lt := range lts {
		if lt.at.Info() == nil {
			return
		}
		if views[li] = viewOf(lt.at, n); views[li].checking {
			return // priorities wait until the start-up check has settled
		}
	}
	if len(t.owner) != n {
		t.owner = make([]*lineTor, n)
	}
	for li, lt := range lts {
		if lt.multi {
			continue
		}
		// The files' priorities give way to the pieces'. A line that was fetching
		// alone keeps what it fetches, its pieces asked for before its files let go:
		// cancelled requests would come again, fetched twice. A line joining starts
		// with nothing and takes from the end of the others' shares.
		fetching := lt.set
		lt.multi, lt.prio, lt.set = true, make([]bool, n), false
		if fetching {
			for i, w := range wantedPieces(lt.at, lt.skip) {
				if w && !views[li].complete[i] && t.owner[i] == nil {
					t.owner[i] = lt
					lt.setPrio(i, true)
				}
			}
		}
		for _, f := range lt.at.Files() {
			f.SetPriority(atorrent.PiecePriorityNone)
		}
	}
	// A piece one line finished is finished for every line: each line reads it
	// from the shared storage, offers it to its peers and can upload it.
	done := make([]bool, n)
	for li := range lts {
		for i, c := range views[li].complete {
			done[i] = done[i] || c
		}
	}
	for li, lt := range lts {
		lt.catchUp(done, views[li].complete)
	}
	if !fetch {
		return
	}
	wanted := wantedPieces(main, skip)
	todo := make([]bool, n)
	left := 0
	for i := range todo {
		if todo[i] = wanted[i] && !done[i]; todo[i] {
			left++
		}
	}

	if left == 0 {
		return
	}
	in := shareIn{owner: make([]int, n), todo: todo, keep: make([]bool, n),
		avail: make([][]bool, len(lts)), alive: make([]bool, len(lts)), rate: make([]float64, len(lts))}
	top := 0.0
	for _, lt := range lts {
		top = max(top, lt.rate.Value())
	}
	for li, lt := range lts {
		if tick%availEvery == 0 || lt.avail == nil {
			lt.avail = availability(lt.at, n)
		}
		in.avail[li] = lt.avail
		in.alive[li] = len(lt.at.PeerConns()) > 0 || time.Since(lt.added) < aliveGrace
		in.rate[li] = max(lt.rate.Value(), top/20, 1) // a slow line keeps a small share, so it can recover
		if lt.rate.Value() == 0 && time.Since(lt.added) < rampUp {
			in.rate[li] = max(top, 1) // not measured yet: the same share as the best
		}
	}
	for i := range n {
		in.owner[i] = -1
		if o := t.owner[i]; o != nil {
			in.owner[i] = slices.Index(lts, o)
		}
		if in.owner[i] < 0 { // a line fetched it before the coordinator had a say (it ran alone): that line's
			in.owner[i] = slices.IndexFunc(views, func(v pieceView) bool { return v.partial[i] })
		}
		// A piece started on a living line stays there; what is half fetched is lost by moving it.
		if o := in.owner[i]; o >= 0 && views[o].partial[i] && in.alive[o] {
			in.keep[i] = true
		}
	}
	next := assignPieces(in) // the line of each piece still to come, -1 for none
	for i, li := range next {
		t.owner[i] = nil
		if li >= 0 {
			t.owner[i] = lts[li]
		}
	}
	// No piece is fetched twice: one line at a time wants a piece, and a piece
	// moving to another line is let go of everywhere before that line asks for it.
	for _, want := range []bool{false, true} {
		for li, lt := range lts {
			if !lt.open() {
				return
			}
			for i := range n {
				if w := todo[i] && next[i] == li; w == want && lt.prio[i] != w {
					lt.setPrio(i, w)
				}
			}
		}
	}
}

// setPrio tells the line to fetch piece i, or not.
func (lt *lineTor) setPrio(i int, want bool) {
	lt.prio[i] = want
	if want {
		lt.at.Piece(i).SetPriority(atorrent.PiecePriorityNormal)
	} else {
		lt.at.Piece(i).SetPriority(atorrent.PiecePriorityNone)
	}
}

// catchUp makes the pieces done on other lines (done) known to this line, which
// has them as have. It reads them from the shared storage. If that still says
// not complete a tick later, it hashes the piece on disk: the storage's record can
// be wrong when the file was promoted from .part as a line asked the storage
// about it, so that the storage found the file missing and marked its pieces not
// complete, though the data is there.
func (lt *lineTor) catchUp(done, have []bool) {
	next := map[int]int{}
	for i, d := range done {
		if !d || have[i] {
			continue
		}
		if !lt.open() {
			break
		}
		// Not pending first: the library checks pending pieces against its request
		// order midway through UpdateCompletion, and panics on a piece still wanted.
		if lt.prio[i] {
			lt.setPrio(i, false)
		}
		p := lt.at.Piece(i)
		p.UpdateCompletion()
		if p.State().Complete {
			continue
		}
		n := lt.lag[i] + 1
		next[i] = n
		if n == 2 || n%20 == 0 {
			go p.VerifyData()
		}
	}
	lt.lag = next
}

// wantedPieces marks the pieces holding any byte of a file not skipped.
func wantedPieces(at *atorrent.Torrent, skip []int) []bool {
	out := make([]bool, at.NumPieces())
	for i, f := range at.Files() {
		if slices.Contains(skip, i) {
			continue
		}
		for p := f.BeginPieceIndex(); p < f.EndPieceIndex() && p < len(out); p++ {
			out[p] = true
		}
	}
	return out
}

// availability is the pieces some peer of the line has.
func availability(at *atorrent.Torrent, n int) []bool {
	out := make([]bool, n)
	for _, pc := range at.PeerConns() {
		bm := pc.PeerPieces()
		if bm.GetCardinality() >= uint64(n) && bm.Contains(0) && bm.Contains(uint32(n-1)) { // a seed
			for i := range out {
				out[i] = true
			}
			return out
		}
		bm.Iterate(func(x uint32) bool {
			if int(x) < n {
				out[x] = true
			}
			return true
		})
	}
	return out
}

// shareIn is what assignPieces needs to give each piece still to come a line.
type shareIn struct {
	owner []int     // the line each piece went to last time, -1 for none
	todo  []bool    // pieces wanted and not finished
	keep  []bool    // pieces that stay with their owner
	avail [][]bool  // per line: pieces some peer of it has
	alive []bool    // per line: it has peers
	rate  []float64 // per line: its speed, above 0
}

// assignPieces gives every piece to do a line, so each line finishes its share about
// when the others do, and moves as few as it can: a piece moved after its line
// asked for it is fetched twice, the chunks already on their way. A piece stays
// with its living owner unless only another line's peers have it. A piece
// without a line goes to one whose peers have it, preferably alive, the one
// that would finish soonest. A living line with nothing left to fetch takes
// pieces from the end of the share of the line that would finish last (that
// line asks for those last), as many as even them out.
func assignPieces(in shareIn) []int {
	k := len(in.rate)
	out := make([]int, len(in.todo))
	load := make([]int, k)
	fetchable := func(l, i int) bool { return in.alive[l] && in.avail[l][i] }
	for i := range out {
		out[i] = -1
		o := in.owner[i]
		if !in.todo[i] || o < 0 {
			continue
		}
		other := false // some living line's peers have it
		for l := range k {
			other = other || fetchable(l, i)
		}
		if in.keep[i] || in.alive[o] && (in.avail[o][i] || !other) {
			out[i] = o
			load[o]++
		}
	}
	finish := func(l int) float64 { return float64(load[l]+1) / in.rate[l] }
	cand := make([]int, 0, k)
	for i := range out {
		if !in.todo[i] || out[i] >= 0 {
			continue
		}
		// The lines that can fetch it, best reason first: alive with a peer that has it, alive, any.
		for level := 0; level < 3 && len(cand) == 0; level++ {
			for l := range k {
				if level == 0 && !(in.alive[l] && in.avail[l][i]) || level == 1 && !in.alive[l] {
					continue
				}
				cand = append(cand, l)
			}
		}
		best := cand[0]
		for _, l := range cand[1:] {
			if finish(l) < finish(best) {
				best = l
			}
		}
		out[i] = best
		load[best]++
		cand = cand[:0]
	}
	for a := range k {
		if !in.alive[a] || load[a] > 0 {
			continue
		}
		d := -1 // the line that would finish last
		for l := range k {
			if l != a && load[l] > 0 && (d < 0 || float64(load[l])/in.rate[l] > float64(load[d])/in.rate[d]) {
				d = l
			}
		}
		if d < 0 {
			continue
		}
		for i := len(out) - 1; i >= 0 && float64(load[a]+1)/in.rate[a] < float64(load[d]-1)/in.rate[d]; i-- {
			if out[i] == d && !in.keep[i] && in.avail[a][i] {
				out[i] = a
				load[a]++
				load[d]--
			}
		}
	}
	return out
}

// received counts a useful block of piece i (n bytes) on the line. The library
// calls it with its locks held: it touches only the torrent's own record. A
// piece another line has received blocks of is fetched twice: the lines don't
// share which blocks each wrote, so the second line fetches them all again.
func (lt *lineTor) received(i, n int) {
	t := lt.t
	t.dupMu.Lock()
	defer t.dupMu.Unlock()
	if i >= len(t.firstLine) {
		t.firstLine = append(t.firstLine, make([]*lineTor, i+1-len(t.firstLine))...)
	}
	switch first := t.firstLine[i]; {
	case first == nil:
		t.firstLine[i] = lt
	case first != lt:
		lt.twice.Add(int64(n))
	}
}
