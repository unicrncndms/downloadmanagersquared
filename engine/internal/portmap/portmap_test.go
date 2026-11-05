package portmap

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// router is a fake home router on loopback, answering NAT-PMP and/or UPnP.
type router struct {
	gw     Gateway
	mu     sync.Mutex
	mapped []string // "TCP 6881->6881 for 127.0.0.1", one per mapping made

	// How NAT-PMP answers, like a VPN server rather than a home router.
	public    int    // grants this public port; 0: the one asked for
	lifetime  uint32 // grants at most this many seconds; 0: what was asked
	refuseUDP bool
}

func (r *router) record(s string) {
	r.mu.Lock()
	r.mapped = append(r.mapped, s)
	r.mu.Unlock()
}

func (r *router) mappings() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.mapped...)
}

func newRouter(t *testing.T, natpmp, upnp bool) *router {
	t.Helper()
	r := &router{}
	lo := net.IPv4(127, 0, 0, 1)
	r.gw = Gateway{
		IP:       lo,
		Local:    lo,
		Listen:   func() (net.PacketConn, error) { return net.ListenPacket("udp4", "127.0.0.1:0") },
		HTTP:     http.DefaultClient,
		ssdpPort: closedPort(t),
		pmpPort:  closedPort(t),
	}
	if natpmp {
		pc := listenUDP(t)
		r.gw.pmpPort = port(pc.LocalAddr())
		go func() {
			buf := make([]byte, 64)
			for {
				n, from, err := pc.ReadFrom(buf)
				if err != nil {
					return
				}
				if n == 2 && buf[0] == 0 && buf[1] == 0 {
					pc.WriteTo([]byte{0, 128, 0, 0, 0, 0, 0, 1, 198, 51, 100, 7}, from)
					continue
				}
				if n != 12 || buf[0] != 0 || (buf[1] != 1 && buf[1] != 2) {
					continue
				}
				internal, external := binary.BigEndian.Uint16(buf[4:]), binary.BigEndian.Uint16(buf[6:])
				life := binary.BigEndian.Uint32(buf[8:])
				proto := map[byte]string{1: "UDP", 2: "TCP"}[buf[1]]
				r.mu.Lock()
				if r.public != 0 {
					external = uint16(r.public)
				}
				if r.lifetime != 0 && r.lifetime < life {
					life = r.lifetime
				}
				refuse := r.refuseUDP && proto == "UDP"
				r.mu.Unlock()
				resp := make([]byte, 16)
				resp[1] = 128 + buf[1]
				if refuse {
					binary.BigEndian.PutUint16(resp[2:], 2) // not authorized
				} else {
					r.record(fmt.Sprintf("%s %d->%d for 127.0.0.1", proto, external, internal))
				}
				binary.BigEndian.PutUint16(resp[8:], internal)
				binary.BigEndian.PutUint16(resp[10:], external)
				binary.BigEndian.PutUint32(resp[12:], life)
				pc.WriteTo(resp, from)
			}
		}()
	}
	if upnp {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			switch req.URL.Path {
			case "/desc.xml":
				fmt.Fprintf(w, `<?xml version="1.0"?><root><device><deviceList><device><deviceList><device><serviceList>
<service><serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType><controlURL>/ctl/IPConn</controlURL></service>
</serviceList></device></deviceList></device></deviceList></device></root>`)
			case "/ctl/IPConn":
				body, _ := io.ReadAll(req.Body)
				b := string(body)
				if !strings.Contains(req.Header.Get("SOAPAction"), "#AddPortMapping") {
					http.Error(w, "bad action", 500)
					return
				}
				r.record(fmt.Sprintf("%s %s->%s for %s", tag(b, "NewProtocol"), tag(b, "NewExternalPort"), tag(b, "NewInternalPort"), tag(b, "NewInternalClient")))
				fmt.Fprint(w, `<?xml version="1.0"?><s:Envelope><s:Body><u:AddPortMappingResponse/></s:Body></s:Envelope>`)
			default:
				http.NotFound(w, req)
			}
		}))
		t.Cleanup(srv.Close)
		ssdp := listenUDP(t)
		r.gw.ssdpPort = port(ssdp.LocalAddr())
		go func() {
			buf := make([]byte, 2048)
			for {
				n, from, err := ssdp.ReadFrom(buf)
				if err != nil {
					return
				}
				if !strings.HasPrefix(string(buf[:n]), "M-SEARCH") {
					continue
				}
				ssdp.WriteTo([]byte("HTTP/1.1 200 OK\r\nST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\nLOCATION: "+srv.URL+"/desc.xml\r\n\r\n"), from)
			}
		}()
	}
	return r
}

func tag(body, name string) string {
	_, after, _ := strings.Cut(body, "<"+name+">")
	v, _, _ := strings.Cut(after, "</"+name+">")
	return v
}

func listenUDP(t *testing.T) net.PacketConn {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	return pc
}

// closedPort is a UDP port nothing listens on.
func closedPort(t *testing.T) int {
	pc := listenUDP(t)
	p := port(pc.LocalAddr())
	pc.Close()
	return p
}

func port(a net.Addr) int { return a.(*net.UDPAddr).Port }

func TestNATPMPMapsTCPAndUDP(t *testing.T) {
	r := newRouter(t, true, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := Map(ctx, r.gw, 6881, time.Hour); err != nil {
		t.Fatal(err)
	}
	if m := strings.Join(r.mappings(), ", "); !strings.Contains(m, "TCP 6881->6881") || !strings.Contains(m, "UDP 6881->6881") {
		t.Fatalf("router mapped %q, want TCP and UDP 6881", m)
	}
}

func TestUPnPMapsWhenNATPMPIsMissing(t *testing.T) {
	r := newRouter(t, false, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := Map(ctx, r.gw, 6881, time.Hour); err != nil {
		t.Fatal(err)
	}
	m := strings.Join(r.mappings(), ", ")
	if !strings.Contains(m, "TCP 6881->6881 for 127.0.0.1") || !strings.Contains(m, "UDP 6881->6881 for 127.0.0.1") {
		t.Fatalf("router mapped %q, want TCP and UDP 6881 to 127.0.0.1", m)
	}
}

func TestNoRouterSupportIsAnError(t *testing.T) {
	r := newRouter(t, false, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := Map(ctx, r.gw, 6881, time.Hour); err == nil {
		t.Fatal("mapping succeeded with neither NAT-PMP nor UPnP")
	}
}

func TestAHomeRouterGrantingAnotherPortIsAnError(t *testing.T) {
	r := newRouter(t, true, false)
	r.public = 40000
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := mapNATPMP(ctx, r.gw, 6881, time.Hour); err == nil || !strings.Contains(err.Error(), "40000") {
		t.Fatalf("got %v, want an error naming the port the router chose", err)
	}
}

func TestMapPMPTakesThePortAndLifetimeTheGatewayGrants(t *testing.T) {
	r := newRouter(t, true, true) // UPnP too: MapPMP must not fall back to it
	r.public, r.lifetime = 51234, 45
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := MapPMP(ctx, r.gw, TCP, 0, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if m.Public != 51234 || m.Lifetime != 45*time.Second || m.Internal != 0 {
		t.Fatalf("mapping %+v, want public 51234 for 45 s", m)
	}
	if got := strings.Join(r.mappings(), ", "); got != "TCP 51234->0 for 127.0.0.1" {
		t.Fatalf("router saw %q, want one NAT-PMP TCP mapping", got)
	}
}

func TestMapPMPRefusedIsAnErrorForThatProtocolOnly(t *testing.T) {
	r := newRouter(t, true, false)
	r.refuseUDP = true
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := MapPMP(ctx, r.gw, UDP, 0, 1, time.Minute); err == nil || !strings.Contains(err.Error(), "result 2") {
		t.Fatalf("UDP: got %v, want the refusal", err)
	}
	if _, err := MapPMP(ctx, r.gw, TCP, 0, 1, time.Minute); err != nil {
		t.Fatalf("TCP: %v", err)
	}
}

func TestExternalAddressProbesForNATPMP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ip, err := ExternalAddress(ctx, newRouter(t, true, false).gw)
	if err != nil || !ip.Equal(net.IPv4(198, 51, 100, 7)) {
		t.Fatalf("got %v, %v, want 198.51.100.7", ip, err)
	}
	start := time.Now()
	if _, err := ExternalAddress(ctx, newRouter(t, false, false).gw); err == nil {
		t.Fatal("a gateway without NAT-PMP answered")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("giving up took %v, want about 2 s", d)
	}
}
