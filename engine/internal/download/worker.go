package download

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"downloadmanagersquared/engine/internal/rate"
)

type worker struct {
	d       *Download
	iface   string
	client  *http.Client
	link    Link
	ir      *ifaceRun
	cancel  context.CancelFunc
	closing bool // cancelled because the interface's connection setting went down
	bytes   atomic.Int64
	ewma    rate.EWMA
	speed   float64
}

// claimLocked gets work from the plan, sized to how fast this interface's
// connections have been.
func (d *Download) claimLocked(ifaceID string) *segment {
	var perConn float64
	if ir := d.ifs[ifaceID]; ir != nil && ir.active > 0 {
		perConn = ir.ewma.Value() / float64(ir.active)
	}
	d.plan.ordered = d.folderOnLocked()
	return d.plan.claim(perConn)
}

func (d *Download) spawnLocked(ctx context.Context, ifc Iface, l Link, seg *segment) {
	wctx, cancel := context.WithCancel(ctx)
	w := &worker{d: d, iface: ifc.ID, client: ifc.clientFor(l), link: l, ir: d.ifRun(ifc.ID), cancel: cancel}
	seg.owner = w
	d.workers[w] = struct{}{}
	w.ir.active++
	d.wg.Add(1)
	go w.run(wctx, seg)
}

// trimLocked closes the connections an interface has beyond its setting, the
// slowest first, so lowering the setting takes effect on a running download.
// Their chunks go back to the plan for the others. Backoff never closes
// working connections; only the setting does.
func (d *Download) trimLocked(usable map[string]Iface) {
	open := map[string][]*worker{}
	for w := range d.workers {
		if !w.closing {
			open[w.iface] = append(open[w.iface], w)
		}
	}
	for id, ws := range open {
		ifc, ok := usable[id]
		extra := len(ws) - max(ifc.Conns, 1)
		if !ok || extra <= 0 {
			continue
		}
		slices.SortFunc(ws, func(a, b *worker) int { return cmp.Compare(a.speed, b.speed) })
		for _, w := range ws[:extra] {
			w.closing = true
			w.cancel()
		}
	}
}

// nextLocked lets a worker that finished a chunk keep going without waiting for a tick.
// A backoff pause only stops new connections; working ones carry on.
func (d *Download) nextLocked(w *worker) *segment {
	l := d.st.Links[w.iface]
	ir := w.ir
	if l == nil || l.Status != LinkReady || l.URL != w.link.URL || ir.active > ir.backoff.target() {
		return nil
	}
	return d.claimLocked(w.iface)
}

func (d *Download) workerFailedLocked(w *worker, err error) {
	now := time.Now()
	ir := w.ir
	l := d.st.Links[w.iface]
	if l != nil {
		defer d.leaveMirrorLocked(w.iface, l)
	}
	var fe *fetchError
	if !errors.As(err, &fe) {
		fe = &fetchError{kind: kindNet, msg: shortErr(err)}
	}
	log.Printf("download %s via %s: %s", d.st.ID, w.iface, fe.msg)
	// A rejection while sibling connections on the same link still work is a
	// per-IP connection limit, not an expired link: back off instead.
	if fe.kind == kindExpired && ir.active > 1 {
		fe = &fetchError{kind: kindBusy, msg: fe.msg, retryAfter: 15 * time.Second}
	}
	switch fe.kind {
	case kindExpired:
		if l != nil && l.URL == w.link.URL && l.Status == LinkReady {
			l.expired(fe.msg)
		}
	case kindBusy:
		ir.backoff.pushedBack(now, fe.retryAfter)
	case kindRange:
		if l != nil && l.URL == w.link.URL {
			l.fail(fe.msg)
		}
	case kindChanged:
		if l != nil && l.URL == w.link.URL {
			l.mismatch(fe.msg)
		}
	case kindDisk:
		select {
		case d.fatal <- errors.New("writing to disk failed: " + fe.msg):
		default:
		}
	default:
		v := ir.backoff.failed(now, fe.kind)
		if l == nil || l.URL != w.link.URL || l.Status != LinkReady {
			break
		}
		switch v {
		case linkUnreachable:
			l.unreachable(now, fe.msg)
		case linkHTTPError:
			l.httpError(now, fe.msg)
		}
	}
}

func (w *worker) run(ctx context.Context, seg *segment) {
	d := w.d
	defer d.wg.Done()
	defer w.cancel()
	buf := make([]byte, bufSize)
	for seg != nil {
		err := w.fetch(ctx, seg, buf)
		d.mu.Lock()
		d.plan.release(seg)
		switch {
		case ctx.Err() != nil:
			seg = nil
		case err != nil:
			d.workerFailedLocked(w, err)
			seg = nil
		default:
			w.ir.backoff.chunkDone()
			if l := d.st.Links[w.iface]; l != nil && l.URL == w.link.URL {
				l.dataArrived()
			}
			if seg = d.nextLocked(w); seg != nil {
				seg.owner = w
			}
		}
		if seg == nil {
			delete(d.workers, w)
			w.ir.active--
		}
		d.mu.Unlock()
	}
}

func (w *worker) fetch(ctx context.Context, seg *segment, buf []byte) error {
	d := w.d
	d.mu.Lock()
	start, end, ranged := seg.pos, seg.end, d.plan.splittable
	size, ver := d.st.Size, d.st.Version
	if d.mirrorLocked(w.link.URL) != nil {
		ver = w.link.Version // each mirror's server has its own ETag and dates
	}
	d.mu.Unlock()
	if start >= end {
		return nil
	}
	if !ranged {
		// Without ranges every attempt fetches the whole file again.
		if err := d.file.Truncate(0); err != nil {
			return &fetchError{kind: kindDisk, msg: err.Error()}
		}
	}

	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stall := time.AfterFunc(stallTimeout, cancel)
	defer stall.Stop()

	req, err := newRequest(rctx, w.link, d.st.UserAgent)
	if err != nil {
		return &fetchError{kind: kindRange, msg: err.Error()}
	}
	if ranged {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end-1))
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if ranged {
		// Before anything else: data from another version must never reach the file.
		if fe := changedError(resp, size, ver); fe != nil {
			return fe
		}
	}
	if fe := statusError(resp); fe != nil {
		return fe
	}
	if ranged {
		if resp.StatusCode != http.StatusPartialContent {
			return &fetchError{kind: kindRange, msg: "server ignored the range request (" + resp.Status + ")"}
		}
		if cs, _, ok := parseContentRange(resp.Header.Get("Content-Range")); !ok || cs != start {
			return &fetchError{kind: kindRange, msg: "server sent the wrong part of the file"}
		}
	}

	ir := w.ir
	for {
		n, rerr := io.ReadFull(resp.Body, buf)
		if n > 0 {
			stall.Reset(stallTimeout)
			d.mu.Lock()
			// The chunk's end may have moved down since the request was sent;
			// never write past it, so nothing is fetched twice into the file.
			off, allowed := d.plan.take(seg, int64(n))
			d.mu.Unlock()
			if allowed > 0 {
				if _, err := d.file.WriteAt(buf[:allowed], off); err != nil {
					return &fetchError{kind: kindDisk, msg: err.Error()}
				}
				d.mu.Lock()
				d.plan.wrote(seg, off, allowed)
				d.mu.Unlock()
				w.bytes.Add(allowed)
				ir.bytes.Add(allowed)
			}
			// Held to the download's own limit; waiting for it isn't a stall.
			stall.Stop()
			if err := d.limit.Took(ctx, n); err != nil {
				return err
			}
			stall.Reset(stallTimeout)
			if allowed < int64(n) {
				return nil
			}
		}
		d.mu.Lock()
		finished := seg.pos >= seg.end
		d.mu.Unlock()
		if finished {
			return nil
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if rctx.Err() != nil {
				return &fetchError{kind: kindNet, msg: "no data for 30s"}
			}
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				if end == unknownEnd {
					d.mu.Lock()
					d.plan.endHere(seg)
					d.mu.Unlock()
					return nil
				}
				return &fetchError{kind: kindNet, msg: "connection closed early"}
			}
			return rerr
		}
	}
}
