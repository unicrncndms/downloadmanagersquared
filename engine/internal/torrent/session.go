// Package torrent fetches torrents over every usable interface at once. Each
// interface (a line) is a peer of its own in a torrent's swarm: it has its own
// anacrolix/torrent client, with its own peer ID, address and port, and the
// client has no socket but that line's. Every peer, DHT packet and tracker
// request goes out through a function the engine supplies for that interface.
// A torrent in the swarm is carried by one client per line over one shared
// storage, and a coordinator splits its pieces between them (lines.go).
package torrent

import (
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/dht/v2"
	atorrent "github.com/anacrolix/torrent"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
	"github.com/anacrolix/utp"

	_ "downloadmanagersquared/engine/internal/torrent/classicio" // before anacrolix's storage init
)

// Iface is an interface torrents may use. Every function must send its traffic
// out through that interface only.
type Iface struct {
	ID           string
	Gen          int  // a new generation (a restarted tunnel, a new address) gets new sockets
	Port         int  // where peers connect in on this interface; 0 = the session's
	Upload       bool // peers may download from us through it
	Dial         func(ctx context.Context, network, addr string) (net.Conn, error)
	ListenPacket func(port int) (net.PacketConn, error) // an IPv4 UDP socket on port, 0 for any free one
	LookupIP     func(ctx context.Context, host string) ([]net.IP, error)
	Listen       func(port int) (net.Listener, error) // optional: TCP for peers connecting in
	// The same over IPv6, only on an interface with IPv6 (Enable IPv6 on); nil otherwise.
	ListenPacket6 func(port int) (net.PacketConn, error)
	Listen6       func(port int) (net.Listener, error)
}

type Config struct {
	Changed      func(*Torrent)       // optional: progress or state changed
	Finished     func(*Torrent)       // optional: the torrent's download completed
	Ratio        func() float64       // optional: seed until uploaded/size reaches this; nil or 0 = don't seed
	PeersPerLine func() int           // optional: connected peers a torrent may have per interface; nil or 0 = the client's 50
	ETAWindow    func() time.Duration // optional: how long the speed behind the time left is averaged over; nil = a minute, as HTTP downloads
	Version      string               // optional: the version peers and trackers are told, as "DMS² <version>"; "" = the torrent library's own name
	DHTBootstrap []string             // optional: host:port of the DHT's first nodes; nil = the usual public ones
	Port         int                  // optional: where peers connect in, TCP and uTP, on each interface that listens

	// Completion, if set, keeps which pieces are complete between runs, for
	// every torrent of the session; the caller closes it after the session.
	// Without it, a torrent's data is hashed at every start.
	Completion storage.PieceCompletion
	// Unclean: the engine last stopped without saving everything, so the
	// pieces Completion has as complete are hashed again at each torrent's
	// first start.
	Unclean bool
}

// completion is a torrent storage's piece completion: the session's kept
// one, which a torrent's storage closing mustn't close, or one of its own.
func (s *Session) completion() storage.PieceCompletion {
	if s.cfg.Completion == nil {
		return storage.NewMapPieceCompletion()
	}
	return keptCompletion{s.cfg.Completion}
}

// keptCompletion is a shared piece completion that Close leaves open.
type keptCompletion struct{ storage.PieceCompletion }

func (keptCompletion) Close() error { return nil }

// ifaceSockets is one line: an interface with its own client and sockets.
type ifaceSockets struct {
	Iface
	s      *Session
	cl     *atorrent.Client // the line's peer: its own peer ID, address and port
	stor   storage.ClientImplCloser
	ipv6   bool                  // the client has IPv6: the interface had IPv6 sockets to give when it opened
	v4, v6 *familySockets        // nil: none of that family (an IPv6-only interface has no v4)
	open   map[*trackedPeer]bool // peers open
	gone   chan struct{}         // closed when the line is no longer used
	done   chan struct{}         // closed once its client has stopped

	opening6 bool // the DHT server being created is the IPv6 one; addIface only

	offline bool // the stand-in line while no interface is usable: a client with no socket, so a torrent still checks its data
}

// familySockets are an interface's sockets of one address family.
type familySockets struct {
	utp  *utp.Socket // uTP peers and DHT share this UDP socket
	dht  *dht.Server
	tcp  net.Listener // nil unless peers can connect in over TCP
	port int          // where peers connect in, 0 if they can't
}

// family is is's sockets of one family, nil if it has none.
func (is *ifaceSockets) family(v6 bool) *familySockets {
	if v6 {
		return is.v6
	}
	return is.v4
}

// Session runs the torrents of one VPN mode. Its lines, and with them every
// socket (peers, DHT, listeners) and client, exist only while a torrent is in
// the swarm, so a session with nothing to do shows nothing on the network.
type Session struct {
	cfg Config

	setMu  sync.Mutex // one change of interfaces, or start or stop of the lines, at a time
	want   []Iface    // the interfaces to use whenever the lines run
	users  int        // torrents in the swarm
	closed bool

	mu    sync.Mutex
	live  bool              // the lines run: a torrent is in the swarm
	ifs   []*ifaceSockets   // the lines, the main one first (it carries web seeds and private torrents)
	watch map[*Torrent]bool // the torrents in the swarm, woken when the lines change
}

func NewSession(cfg Config) (*Session, error) {
	return &Session{cfg: cfg, watch: map[*Torrent]bool{}}, nil
}

// newClient makes a line's client, which opens no socket of its own.
func (s *Session) newClient(is *ifaceSockets) (*atorrent.Client, storage.ClientImplCloser, error) {
	c := atorrent.NewDefaultClientConfig()
	// No built-in sockets: everything below is added for the line.
	c.DisableTCP = true
	c.DisableUTP = true
	c.NoDHT = true
	c.DisableIPv6 = !is.ipv6 // with IPv6 off no IPv6 peer is even tried
	c.NoDefaultPortForwarding = true
	c.DisableWebtorrent = true // WebRTC opens its own UDP sockets
	c.ListenPort = 0
	if v := s.cfg.Version; v != "" {
		// Not the library's default, which names the Go module path and "(devel)".
		c.ExtendedHandshakeClientVersion = "DMS² " + v
		c.HTTPUserAgent = "DMS2/" + v // header values stay ASCII
	}
	c.PieceHashersPerTorrent = 4 // the default 2 caps verifying a finished torrent on a fast disk
	c.Seed = true                // without it, peers are dropped on completion; Torrent decides how long to seed
	c.TrackerDialContext = is.Dial
	c.HTTPDialContext = is.Dial
	c.TrackerListenPacket = func(network, _ string) (net.PacketConn, error) {
		if is.ipv6 && is.v6 != nil && (network == "udp6" || is.v4 == nil) {
			return is.ListenPacket6(0)
		}
		return is.ListenPacket(0)
	}
	c.ConfigureAnacrolixDhtServer = func(dc *dht.ServerConfig) {
		v6, hosts := is.opening6, s.bootstrap()
		dc.StartingNodes = func() ([]dht.Addr, error) { return is.bootstrapNodes(hosts, v6) }
	}
	// Torrents announce themselves (Torrent.announceDHT), so private ones can stay off the DHT.
	c.PeriodicallyAnnounceTorrentsToDht = false
	// The client knows nothing of private torrents: keep PEX off theirs both ways.
	c.Callbacks.PeerConnAdded = append(c.Callbacks.PeerConnAdded, func(pc *atorrent.PeerConn) {
		if private(pc.Torrent()) {
			pc.LocalLtepProtocolMap = withoutPEX(pc.LocalLtepProtocolMap)
		}
	})
	c.Callbacks.ReadExtendedHandshake = func(pc *atorrent.PeerConn, m *pp.ExtendedHandshakeMessage) {
		if private(pc.Torrent()) {
			delete(m.M, pp.ExtensionNamePex)
		}
	}
	// Each torrent gets its own storage; this only keeps the client from creating
	// a piece database in the working directory.
	stor := storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir: os.TempDir(), PieceCompletion: storage.NewMapPieceCompletion(),
		Logger: slog.New(slog.DiscardHandler),
	})
	c.DefaultStorage = stor
	c.Slogger = slog.New(slog.NewTextHandler(io.Discard, nil))
	cl, err := atorrent.NewClient(c)
	if err != nil {
		stor.Close()
		return nil, nil, err
	}
	cl.AddDialer(peerDialer{is, "tcp4"})
	cl.AddDialer(peerDialer{is, "udp4"})
	if is.ipv6 {
		cl.AddDialer(peerDialer{is, "tcp6"})
		cl.AddDialer(peerDialer{is, "udp6"})
	}
	return cl, stor, nil
}

var (
	errClosed   = errors.New("the torrent session is closed")
	errPeerGone = errors.New("the peer's tunnel closed as it connected")
)

// acquire is a torrent joining the swarm: it starts the lines, on the
// interfaces SetIfaces gave, if no other torrent has, and will tell t when they
// change. Every acquire is matched by one release.
func (s *Session) acquire(t *Torrent) error {
	s.setMu.Lock()
	defer s.setMu.Unlock()
	if s.closed {
		return errClosed
	}
	if s.users == 0 {
		s.mu.Lock()
		s.live = true
		s.mu.Unlock()
		s.applyLocked(s.want)
	}
	s.users++
	s.mu.Lock()
	s.watch[t] = true
	s.mu.Unlock()
	return nil
}

// release is a torrent leaving the swarm; the last one stops the lines and
// closes every socket.
func (s *Session) release(t *Torrent) {
	s.setMu.Lock()
	defer s.setMu.Unlock()
	s.mu.Lock()
	delete(s.watch, t)
	s.mu.Unlock()
	if s.users--; s.users == 0 {
		s.stopLinesLocked()
	}
}

func (s *Session) stopLinesLocked() {
	s.mu.Lock()
	ifs := s.ifs
	s.ifs, s.live = nil, false
	s.mu.Unlock()
	for _, is := range ifs {
		is.close()
	}
}

// Live is whether the session is on the network: a torrent is in the swarm.
func (s *Session) Live() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live
}

// lines are the lines now, the main one first.
func (s *Session) lines() []*ifaceSockets {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ifs)
}

var (
	errNoIface     = errors.New("no usable interface")
	errOtherFamily = errors.New("an address of the other family")
)

// SetIfaces makes ifaces the interfaces torrents use, in that order (the first
// is the main line, which carries web seeds and private torrents). While the
// lines run they are matched by ID: new ones get their client and sockets, ones
// no longer listed close theirs, and the rest switch upload on their open
// peers. Torrents in the swarm follow the change within a moment.
func (s *Session) SetIfaces(ifaces []Iface) {
	s.setMu.Lock()
	defer s.setMu.Unlock()
	s.want = ifaces
	if s.users > 0 {
		s.applyLocked(ifaces)
	}
}

func (s *Session) applyLocked(ifaces []Iface) {
	want := map[string]Iface{}
	for _, ifc := range ifaces {
		want[ifc.ID] = ifc
	}
	s.mu.Lock()
	have := map[string]bool{}
	var gone []*ifaceSockets
	var switching []*trackedPeer
	kept := s.ifs[:0]
	for _, is := range s.ifs {
		if is.offline { // stays until a real line opens
			kept = append(kept, is)
			continue
		}
		if w, ok := want[is.ID]; ok && w.Gen == is.Gen && w.Port == is.Iface.Port {
			kept = append(kept, is)
			have[is.ID] = true
			if w.Upload != is.Upload {
				is.Upload = w.Upload
				for p := range is.open {
					switching = append(switching, p)
				}
			}
		} else {
			gone = append(gone, is)
		}
	}
	s.ifs = kept
	s.mu.Unlock()
	for _, p := range switching {
		p.gate.SetUpload(p.is.Upload)
	}
	for _, is := range gone {
		is.close() // its client closes with its peers
	}
	pos := map[string]int{}
	for i, ifc := range ifaces {
		pos[ifc.ID] = i
		if !have[ifc.ID] {
			if err := s.addIface(ifc); err != nil {
				log.Printf("torrent sockets for %s: %v", ifc.ID, err)
			}
		}
	}
	s.mu.Lock()
	slices.SortFunc(s.ifs, func(a, b *ifaceSockets) int { return pos[a.ID] - pos[b.ID] })
	idle := slices.IndexFunc(s.ifs, func(is *ifaceSockets) bool { return is.offline })
	n := len(s.ifs)
	if idle >= 0 && n > 1 {
		gone = append(gone[:0], s.ifs[idle])
		s.ifs = slices.Delete(s.ifs, idle, idle+1)
	} else {
		gone = nil
	}
	watching := make([]*Torrent, 0, len(s.watch))
	for t := range s.watch {
		watching = append(watching, t)
	}
	s.mu.Unlock()
	for _, is := range gone {
		is.close()
	}
	if n == 0 {
		s.addOffline()
	}
	for _, t := range watching {
		t.wakeUp()
	}
}

// addOffline adds the stand-in line, when no interface is usable.
func (s *Session) addOffline() {
	is := &ifaceSockets{s: s, offline: true, open: map[*trackedPeer]bool{}, gone: make(chan struct{}), done: make(chan struct{})}
	is.Iface = Iface{
		Dial:         func(context.Context, string, string) (net.Conn, error) { return nil, errNoIface },
		LookupIP:     func(context.Context, string) ([]net.IP, error) { return nil, errNoIface },
		ListenPacket: func(int) (net.PacketConn, error) { return nil, errNoIface },
	}
	cl, stor, err := s.newClient(is)
	if err != nil {
		log.Printf("torrent client with no interface: %v", err)
		return
	}
	is.cl, is.stor = cl, stor
	s.mu.Lock()
	s.ifs = append(s.ifs, is)
	s.mu.Unlock()
}

// addIface opens a line: its client and sockets.
func (s *Session) addIface(ifc Iface) error {
	want := s.cfg.Port
	if ifc.Port != 0 {
		want = ifc.Port
	}
	is := &ifaceSockets{Iface: ifc, s: s, ipv6: ifc.ListenPacket6 != nil,
		open: map[*trackedPeer]bool{}, gone: make(chan struct{}), done: make(chan struct{})}
	cl, stor, err := s.newClient(is)
	if err != nil {
		return err
	}
	is.cl, is.stor = cl, stor
	var errs []error
	for _, v6 := range []bool{false, true} {
		listenPacket, listen := ifc.ListenPacket, ifc.Listen
		if v6 {
			if !is.ipv6 {
				continue
			}
			listenPacket, listen = ifc.ListenPacket6, ifc.Listen6
		}
		f, err := s.openFamily(is, v6, listenPacket, listen, want)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if v6 {
			is.v6 = f
		} else {
			is.v4 = f
		}
	}
	if is.v4 == nil && is.v6 == nil {
		cl.Close()
		stor.Close()
		return errors.Join(errs...)
	}
	for _, f := range []*familySockets{is.v4, is.v6} {
		if f == nil {
			continue
		}
		cl.AddDhtServer(atorrent.AnacrolixDhtServerWrapper{Server: f.dht})
		if f.port != 0 {
			cl.AddListener(&peerListener{s: s, is: is, l: f.tcp, addr: addrOr(f.tcp.Addr(), "tcp", ""), done: is.done})
			cl.AddListener(&peerListener{s: s, is: is, l: f.utp, addr: addrOr(f.utp.Addr(), "udp", ""), done: is.done})
		}
	}
	s.mu.Lock()
	s.ifs = append(s.ifs, is)
	s.mu.Unlock()
	return nil
}

// openFamily opens is's uTP socket and DHT server of one family, and its TCP
// listener on port want when peers may connect in (listen set, want not 0).
func (s *Session) openFamily(is *ifaceSockets, v6 bool, listenPacket func(int) (net.PacketConn, error), listen func(int) (net.Listener, error), want int) (*familySockets, error) {
	incoming := listen != nil && want != 0
	port := 0
	if incoming {
		port = want
	}
	pc, err := listenPacket(port)
	if err != nil && port != 0 {
		log.Printf("torrent port %d on %s: %v; outgoing peers only", port, is.ID, err)
		incoming = false
		pc, err = listenPacket(0)
	}
	if err != nil {
		return nil, err
	}
	us, err := utp.NewSocketFromPacketConn(pc)
	if err != nil {
		pc.Close()
		return nil, err
	}
	f := &familySockets{utp: us}
	if incoming {
		if f.tcp, err = listen(port); err != nil {
			log.Printf("torrent port %d on %s: %v; outgoing peers only", port, is.ID, err)
			incoming = false
		}
	}
	is.opening6 = v6
	f.dht, err = is.cl.NewAnacrolixDhtServer(us)
	if err != nil {
		f.close()
		return nil, err
	}
	if incoming {
		f.port = port
	}
	return f, nil
}

// portedDHT announces on one interface's DHT with that interface's port. The
// client states one port for all (or lets the DHT take our UDP source port,
// which a VPN's NAT needn't keep); a forwarded port differs per tunnel.
type portedDHT struct {
	atorrent.DhtServer
	port int // 0: announce as asked
}

func (d portedDHT) Announce(hash [20]byte, port int, impliedPort bool) (atorrent.DhtAnnounce, error) {
	if d.port != 0 {
		port, impliedPort = d.port, false
	}
	return d.DhtServer.Announce(hash, port, impliedPort)
}

// close stops the line's client, which closes its torrents and peers, then its sockets.
func (is *ifaceSockets) close() {
	// The client first: its accept loops end on a closed client, not on closed sockets.
	is.cl.Close()
	is.stor.Close()
	close(is.done)
	close(is.gone)
	for _, f := range []*familySockets{is.v4, is.v6} {
		if f != nil {
			f.close()
		}
	}
}

func (f *familySockets) close() {
	if f.dht != nil {
		f.dht.Close()
	}
	f.utp.Close()
	if f.tcp != nil {
		f.tcp.Close()
	}
}

// Port is where peers connect in, 0 if nowhere.
func (s *Session) Port() int { return s.cfg.Port }

// peerListener takes peers connecting in on one interface.
type peerListener struct {
	s    *Session
	is   *ifaceSockets
	done chan struct{} // its client's
	l    interface {
		Accept() (net.Conn, error)
		Addr() net.Addr
	}
	addr net.Addr // as opened: see addrOr
}

func (l *peerListener) Addr() net.Addr { return l.addr }

// addrOr is a, or a stand-in when a is nil. A tunnel's listeners and
// connections answer nil for their addresses once its stack is closed, and the
// torrent client calls Network and String on them without checking (a panic
// that took the engine down when a tunnel restarted as a peer connected in).
func addrOr(a net.Addr, network, s string) net.Addr {
	if a != nil {
		return a
	}
	return stubAddr{network, s}
}

type stubAddr struct{ network, s string }

func (a stubAddr) Network() string { return a.network }
func (a stubAddr) String() string  { return a.s }

func (l *peerListener) Accept() (net.Conn, error) {
	c, err := l.l.Accept()
	if err != nil {
		select {
		case <-l.is.gone:
			// The client can't drop a listener and retries a failed accept at once:
			// park it until the client closes.
			<-l.done
		default:
		}
		return nil, err
	}
	remote := c.RemoteAddr()
	if remote == nil { // its tunnel closed as it came in
		c.Close()
		return nil, errPeerGone
	}
	l.s.mu.Lock()
	defer l.s.mu.Unlock()
	return l.s.trackLocked(remote.String(), l.is, c), nil
}

func (s *Session) bootstrap() []string {
	if s.cfg.DHTBootstrap != nil {
		return s.cfg.DHTBootstrap
	}
	return dht.DefaultGlobalBootstrapHostPorts
}

// private is whether the torrent's info marks it private. Unknown (a magnet
// link without its info yet) counts as public.
func private(at *atorrent.Torrent) bool {
	info := at.Info()
	return info != nil && info.Private != nil && *info.Private
}

// withoutPEX is m without the PEX extension, so it isn't offered to the peer.
func withoutPEX(m *atorrent.LocalLtepProtocolMap) *atorrent.LocalLtepProtocolMap {
	out := &atorrent.LocalLtepProtocolMap{}
	for i, name := range m.Index {
		if name == pp.ExtensionNamePex {
			continue
		}
		out.Index = append(out.Index, name)
		if i < m.NumBuiltin {
			out.NumBuiltin++
		}
	}
	return out
}

// dhtServers are the DHT servers of the line.
func (is *ifaceSockets) dhtServers() []atorrent.DhtServer {
	var out []atorrent.DhtServer
	for _, f := range []*familySockets{is.v4, is.v6} {
		if f != nil {
			out = append(out, portedDHT{atorrent.AnacrolixDhtServerWrapper{Server: f.dht}, f.port})
		}
	}
	return out
}

// bootstrapNodes resolves the DHT's bootstrap hosts through this interface's
// DNS, keeping the addresses of one family.
func (is *ifaceSockets) bootstrapNodes(hosts []string, v6 bool) ([]dht.Addr, error) {
	network := "udp4"
	if v6 {
		network = "udp6"
	}
	var addrs []dht.Addr
	for _, hp := range hosts {
		host, port, err := net.SplitHostPort(hp)
		if err != nil {
			continue
		}
		ips, err := is.LookupIP(context.Background(), host)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			if (ip.To4() == nil) != v6 {
				continue
			}
			if ua, err := net.ResolveUDPAddr(network, net.JoinHostPort(ip.String(), port)); err == nil {
				addrs = append(addrs, dht.NewAddr(ua))
			}
		}
	}
	if len(addrs) == 0 {
		return nil, errors.New("no DHT bootstrap node resolved")
	}
	return addrs, nil
}

// trackLocked counts an open peer against its line until it closes.
func (s *Session) trackLocked(addr string, is *ifaceSockets, c net.Conn) *trackedPeer {
	g := newUploadGate(c, is.Upload)
	p := &trackedPeer{Conn: g, gate: g, s: s, is: is,
		local: addrOr(c.LocalAddr(), "tcp", ""), remote: addrOr(c.RemoteAddr(), "tcp", addr)}
	is.open[p] = true
	return p
}

func (s *Session) peerClosed(p *trackedPeer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(p.is.open, p)
}

// peerDialer is a line client's only dialer for one network.
type peerDialer struct {
	is      *ifaceSockets
	network string
}

func (d peerDialer) DialerNetwork() string { return d.network }

func (d peerDialer) Dial(ctx context.Context, addr string) (net.Conn, error) {
	v6 := strings.HasSuffix(d.network, "6")
	host, _, err := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); err != nil || ip == nil || (ip.To4() == nil) != v6 {
		return nil, errOtherFamily // the dialer of the other family takes it
	}
	f := d.is.family(v6)
	if f == nil {
		return nil, errNoIface
	}
	var c net.Conn
	if strings.HasPrefix(d.network, "udp") {
		c, err = f.utp.DialContext(ctx, d.network, addr)
	} else {
		c, err = d.is.Dial(ctx, d.network, addr)
	}
	if err != nil {
		return nil, err
	}
	s := d.is.s
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trackLocked(addr, d.is, c), nil
}

// trackedPeer is a peer's socket, counted against its line until closed.
type trackedPeer struct {
	net.Conn
	gate *uploadGate
	s    *Session
	is   *ifaceSockets // Upload read under s.mu
	once sync.Once

	local, remote net.Addr // as connected: see addrOr
}

func (p *trackedPeer) LocalAddr() net.Addr  { return p.local }
func (p *trackedPeer) RemoteAddr() net.Addr { return p.remote }

func (p *trackedPeer) Close() error {
	p.once.Do(func() { p.s.peerClosed(p) })
	return p.Conn.Close()
}

// ifacePeers are a torrent's connected peers on one line, and the piece data
// they have moved since they connected.
type ifacePeers struct {
	n             int
	read, written int64
	sent, wasted  int64 // since the line joined: what its peers sent, and what of it wasn't used
}

// peers counts the line's connected peers of the torrent.
func (lt *lineTor) peers() ifacePeers {
	var out ifacePeers
	for _, pc := range lt.at.PeerConns() {
		st := pc.Stats()
		out.n++
		out.read += st.BytesReadData.Int64()
		out.written += st.BytesWrittenData.Int64()
	}
	st := lt.at.Stats() // the torrent's: peers that left count too
	out.sent = st.BytesReadData.Int64()
	out.wasted = max(out.sent-st.BytesReadUsefulData.Int64(), 0)
	return out
}

// Close closes every line and its sockets. It does not stop torrents: the
// caller stops them first (the engine's queue does), as each one's run ends with its context.
func (s *Session) Close() {
	s.setMu.Lock()
	defer s.setMu.Unlock()
	s.closed = true
	if s.users > 0 || s.live {
		s.stopLinesLocked()
	}
}
