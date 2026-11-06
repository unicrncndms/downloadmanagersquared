// Package torrent fetches torrents over every usable interface at once. It wraps
// one anacrolix/torrent client per engine and gives it no socket of its own:
// every peer, DHT packet and tracker request goes out through a function the
// engine supplies for one interface.
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
	"sync"

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
}

type Config struct {
	Changed      func(*Torrent) // optional: progress or state changed
	Finished     func(*Torrent) // optional: the torrent's download completed
	Ratio        func() float64 // optional: seed until uploaded/size reaches this; nil or 0 = don't seed
	DHTBootstrap []string       // optional: host:port of the DHT's first nodes; nil = the usual public ones
	Port         int            // optional: where peers connect in, TCP and uTP, on each interface that listens
}

// ifaceSockets is one interface as the session uses it.
type ifaceSockets struct {
	Iface
	utp   *utp.Socket // uTP peers and DHT share this UDP socket
	dht   *dht.Server
	tcp   net.Listener          // nil unless peers can connect in over TCP
	port  int                   // where peers connect in, 0 if they can't
	peers int                   // peers open or being dialed
	open  map[*trackedPeer]bool // peers open
	gone  chan struct{}         // closed when the interface is no longer used
}

// dialing is the interface every dial to one peer address goes out on, while any is in flight.
type dialing struct {
	is *ifaceSockets
	n  int
}

// Session runs the torrents of one VPN mode. Its client, and with it every
// socket (peers, DHT, listeners), exists only while a torrent is in the swarm,
// so a session with nothing to do shows nothing on the network.
type Session struct {
	cfg Config

	setMu  sync.Mutex // one change of interfaces, or start or stop of the client, at a time
	want   []Iface    // the interfaces to use whenever the client runs
	users  int        // torrents in the swarm
	closed bool

	mu      sync.Mutex
	cl      *atorrent.Client // nil while no torrent is in the swarm
	stor    storage.ClientImplCloser
	clDone  chan struct{}   // closed once the current client has stopped
	ifs     []*ifaceSockets // the first carries trackers and web seeds
	dialing map[string]*dialing
	peerAt  map[string][]*ifaceSockets // peer address -> interfaces of its open peers
	setup   *ifaceSockets              // interface whose DHT server is being created
}

func NewSession(cfg Config) (*Session, error) {
	return &Session{cfg: cfg, dialing: map[string]*dialing{}, peerAt: map[string][]*ifaceSockets{}}, nil
}

// newClient makes the client, which opens no socket of its own.
func (s *Session) newClient() (*atorrent.Client, storage.ClientImplCloser, error) {
	c := atorrent.NewDefaultClientConfig()
	// No built-in sockets: everything below is added per interface.
	c.DisableTCP = true
	c.DisableUTP = true
	c.NoDHT = true
	c.DisableIPv6 = true
	c.NoDefaultPortForwarding = true
	c.DisableWebtorrent = true // WebRTC opens its own UDP sockets
	c.ListenPort = 0
	c.Seed = true // without it, peers are dropped on completion; Torrent decides how long to seed
	c.TrackerDialContext = s.dialFirst
	c.HTTPDialContext = s.dialFirst
	c.TrackerListenPacket = func(string, string) (net.PacketConn, error) {
		is := s.first()
		if is == nil {
			return nil, errNoIface
		}
		return is.ListenPacket(0)
	}
	c.ConfigureAnacrolixDhtServer = func(dc *dht.ServerConfig) {
		is, hosts := s.setup, s.bootstrap()
		dc.StartingNodes = func() ([]dht.Addr, error) { return is.bootstrapNodes(hosts) }
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
	cl.AddDialer(peerDialer{s, "tcp4"})
	cl.AddDialer(peerDialer{s, "udp4"})
	return cl, stor, nil
}

var errClosed = errors.New("the torrent session is closed")

// acquire is a torrent joining the swarm: it starts the client, on the
// interfaces SetIfaces gave, if no other torrent has. Every acquire is
// matched by one release.
func (s *Session) acquire() (*atorrent.Client, error) {
	s.setMu.Lock()
	defer s.setMu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	if s.cl == nil {
		cl, stor, err := s.newClient()
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.cl, s.stor, s.clDone = cl, stor, make(chan struct{})
		s.mu.Unlock()
		s.applyLocked(s.want)
	}
	s.users++
	return s.cl, nil
}

// release is a torrent leaving the swarm; the last one stops the client and
// closes every socket.
func (s *Session) release() {
	s.setMu.Lock()
	defer s.setMu.Unlock()
	if s.users--; s.users == 0 && s.cl != nil {
		s.stopClientLocked()
	}
}

func (s *Session) stopClientLocked() {
	s.mu.Lock()
	cl, stor, done, ifs := s.cl, s.stor, s.clDone, s.ifs
	s.cl, s.stor, s.ifs = nil, nil, nil
	s.dialing, s.peerAt = map[string]*dialing{}, map[string][]*ifaceSockets{}
	s.mu.Unlock()
	// The client first: its accept loops end on a closed client, not on closed sockets.
	cl.Close()
	stor.Close()
	close(done)
	for _, is := range ifs {
		is.close()
	}
}

// Live is whether the session is on the network: a torrent is in the swarm.
func (s *Session) Live() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cl != nil
}

var errNoIface = errors.New("no usable interface")

// SetIfaces makes ifaces the interfaces torrents use, in that order (the first
// carries trackers and web seeds). While the client runs they are matched by
// ID: new ones get their sockets, ones no longer listed close their peers and
// sockets, and the rest switch upload on their open peers. The client can't
// forget a DHT server, so a removed interface's closed server stays in its list
// and fails its announces.
func (s *Session) SetIfaces(ifaces []Iface) {
	s.setMu.Lock()
	defer s.setMu.Unlock()
	s.want = ifaces
	if s.cl != nil {
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
	var closing, switching []*trackedPeer
	kept := s.ifs[:0]
	for _, is := range s.ifs {
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
			for p := range is.open {
				closing = append(closing, p)
			}
		}
	}
	s.ifs = kept
	s.mu.Unlock()
	for _, p := range closing {
		p.Close()
	}
	for _, p := range switching {
		p.gate.SetUpload(p.is.Upload)
	}
	for _, is := range gone {
		is.close()
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
	s.mu.Unlock()
}

func (s *Session) addIface(ifc Iface) error {
	want := s.cfg.Port
	if ifc.Port != 0 {
		want = ifc.Port
	}
	incoming := ifc.Listen != nil && want != 0
	port := 0
	if incoming {
		port = want
	}
	pc, err := ifc.ListenPacket(port)
	if err != nil && port != 0 {
		log.Printf("torrent port %d on %s: %v; outgoing peers only", port, ifc.ID, err)
		incoming = false
		pc, err = ifc.ListenPacket(0)
	}
	if err != nil {
		return err
	}
	us, err := utp.NewSocketFromPacketConn(pc)
	if err != nil {
		pc.Close()
		return err
	}
	is := &ifaceSockets{Iface: ifc, utp: us, open: map[*trackedPeer]bool{}, gone: make(chan struct{})}
	if incoming {
		if is.tcp, err = ifc.Listen(port); err != nil {
			log.Printf("torrent port %d on %s: %v; outgoing peers only", port, ifc.ID, err)
			incoming = false
		}
	}
	cl := s.cl // set while setMu is held
	s.mu.Lock()
	s.setup = is
	ds, err := cl.NewAnacrolixDhtServer(us)
	s.setup = nil
	if err == nil {
		is.dht = ds
		s.ifs = append(s.ifs, is)
	}
	s.mu.Unlock()
	if err != nil {
		us.Close()
		if is.tcp != nil {
			is.tcp.Close()
		}
		return err
	}
	cl.AddDhtServer(atorrent.AnacrolixDhtServerWrapper{Server: ds})
	if incoming {
		is.port = port
		cl.AddListener(&peerListener{s: s, is: is, l: is.tcp, done: s.clDone})
		cl.AddListener(&peerListener{s: s, is: is, l: us, done: s.clDone})
	}
	return nil
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

func (is *ifaceSockets) close() {
	close(is.gone)
	is.dht.Close()
	is.utp.Close()
	if is.tcp != nil {
		is.tcp.Close()
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
}

func (l *peerListener) Addr() net.Addr { return l.l.Addr() }

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
	l.s.mu.Lock()
	defer l.s.mu.Unlock()
	l.is.peers++
	return l.s.trackLocked(c.RemoteAddr().String(), l.is, c), nil
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

// dhtServers are the DHT servers of the interfaces in use.
func (s *Session) dhtServers() []atorrent.DhtServer {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []atorrent.DhtServer
	for _, is := range s.ifs {
		out = append(out, portedDHT{atorrent.AnacrolixDhtServerWrapper{Server: is.dht}, is.port})
	}
	return out
}

// bootstrapNodes resolves the DHT's bootstrap hosts through this interface's DNS.
func (is *ifaceSockets) bootstrapNodes(hosts []string) ([]dht.Addr, error) {
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
			if ua, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(ip.String(), port)); err == nil {
				addrs = append(addrs, dht.NewAddr(ua))
			}
		}
	}
	if len(addrs) == 0 {
		return nil, errors.New("no DHT bootstrap node resolved")
	}
	return addrs, nil
}

// first is the interface for trackers and web seeds, which need one
// connection, not a spread.
func (s *Session) first() *ifaceSockets {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ifs) == 0 {
		return nil
	}
	return s.ifs[0]
}

func (s *Session) dialFirst(ctx context.Context, network, addr string) (net.Conn, error) {
	is := s.first()
	if is == nil {
		return nil, errNoIface
	}
	return is.Dial(ctx, network, addr)
}

// lookupFirst resolves a tracker's hostname through the interface trackers use.
func (s *Session) lookupFirst(ctx context.Context, host string) ([]net.IP, error) {
	is := s.first()
	if is == nil {
		return nil, errNoIface
	}
	return is.LookupIP(ctx, host)
}

// pick chooses the interface for a new peer: the one carrying the fewest peers.
// Dials to the same address (TCP and uTP race each other) share an interface.
func (s *Session) pick(addr string) *ifaceSockets {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.dialing[addr]
	if d == nil {
		var best *ifaceSockets
		for _, is := range s.ifs {
			if best == nil || is.peers < best.peers {
				best = is
			}
		}
		if best == nil {
			return nil
		}
		d = &dialing{is: best}
		s.dialing[addr] = d
	}
	d.n++
	d.is.peers++
	return d.is
}

// dialed records how a dial chosen by pick ended; c is nil if it failed.
func (s *Session) dialed(addr string, is *ifaceSockets, c net.Conn) net.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.dialing[addr]; d != nil {
		if d.n--; d.n == 0 {
			delete(s.dialing, addr)
		}
	}
	if c == nil {
		is.peers--
		return nil
	}
	return s.trackLocked(addr, is, c)
}

// trackLocked counts an open peer against its interface until it closes.
func (s *Session) trackLocked(addr string, is *ifaceSockets, c net.Conn) *trackedPeer {
	s.peerAt[addr] = append(s.peerAt[addr], is)
	g := newUploadGate(c, is.Upload)
	p := &trackedPeer{Conn: g, gate: g, s: s, addr: addr, is: is}
	is.open[p] = true
	return p
}

func (s *Session) peerClosed(p *trackedPeer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	addr, is := p.addr, p.is
	delete(is.open, p)
	is.peers--
	at := s.peerAt[addr]
	if i := slices.Index(at, is); i >= 0 {
		at = slices.Delete(at, i, i+1)
	}
	if len(at) == 0 {
		delete(s.peerAt, addr)
	} else {
		s.peerAt[addr] = at
	}
}

// peerDialer is the client's only dialer for one network. The client dials every
// peer on all its dialers at once, so choosing the interface happens here.
type peerDialer struct {
	s       *Session
	network string
}

func (d peerDialer) DialerNetwork() string { return d.network }

func (d peerDialer) Dial(ctx context.Context, addr string) (net.Conn, error) {
	is := d.s.pick(addr)
	if is == nil {
		return nil, errNoIface
	}
	var c net.Conn
	var err error
	if d.network == "udp4" {
		c, err = is.utp.DialContext(ctx, "udp4", addr)
	} else {
		c, err = is.Dial(ctx, d.network, addr)
	}
	if err != nil {
		c = nil
	}
	if p := d.s.dialed(addr, is, c); p != nil {
		return p, nil
	}
	return nil, err
}

// trackedPeer is a peer's socket, counted against its interface until closed.
type trackedPeer struct {
	net.Conn
	gate *uploadGate
	s    *Session
	addr string        // as dialed, which is how the client names the peer
	is   *ifaceSockets // Upload read under s.mu
	once sync.Once
}

func (p *trackedPeer) Close() error {
	p.once.Do(func() { p.s.peerClosed(p) })
	return p.Conn.Close()
}

// peersByIface counts a torrent's connected peers on each interface. A peer
// address open on several interfaces (two torrents sharing a peer) counts once,
// on the first.
// ifacePeers are a torrent's connected peers on one interface, and the piece
// data they have moved since they connected.
type ifacePeers struct {
	n             int
	read, written int64
}

// peersByIface counts a torrent's connected peers on each interface. A peer
// address open on several interfaces (two torrents sharing a peer) counts once,
// on the first.
func (s *Session) peersByIface(at *atorrent.Torrent) map[string]ifacePeers {
	type peer struct {
		addr          string
		read, written int64
	}
	var peers []peer
	for _, pc := range at.PeerConns() {
		st := pc.Stats()
		peers = append(peers, peer{pc.RemoteAddr.String(), st.BytesReadData.Int64(), st.BytesWrittenData.Int64()})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]ifacePeers{}
	for _, p := range peers {
		if at := s.peerAt[p.addr]; len(at) > 0 {
			ip := out[at[0].ID]
			ip.n, ip.read, ip.written = ip.n+1, ip.read+p.read, ip.written+p.written
			out[at[0].ID] = ip
		}
	}
	return out
}

// Close stops every torrent and closes every socket.
func (s *Session) Close() {
	s.setMu.Lock()
	defer s.setMu.Unlock()
	s.closed = true
	if s.cl != nil {
		s.stopClientLocked()
	}
}
