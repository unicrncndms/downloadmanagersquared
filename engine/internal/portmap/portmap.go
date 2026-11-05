// Package portmap asks the router behind one interface to forward a port to
// it, with NAT-PMP or else UPnP, so torrent peers can connect in. Everything
// it sends goes through the functions the Gateway supplies for that interface.
package portmap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Gateway is one interface's router, and how to reach it through that interface.
type Gateway struct {
	IP     net.IP                         // the router
	Local  net.IP                         // our address on the interface, which the port is forwarded to
	Listen func() (net.PacketConn, error) // a UDP socket on the interface
	HTTP   *http.Client

	ssdpPort, pmpPort int // 0: the standard ones; tests point them elsewhere
}

// Map forwards port, TCP and UDP, from the router to the same port here, for
// lifetime (map again before it runs out).
func Map(ctx context.Context, gw Gateway, port int, lifetime time.Duration) error {
	if gw.ssdpPort == 0 {
		gw.ssdpPort = 1900
	}
	if gw.pmpPort == 0 {
		gw.pmpPort = 5351
	}
	pmpErr := mapNATPMP(ctx, gw, port, lifetime)
	if pmpErr == nil {
		return nil
	}
	upnpErr := mapUPnP(ctx, gw, port, lifetime)
	if upnpErr == nil {
		return nil
	}
	return fmt.Errorf("NAT-PMP: %v; UPnP: %v", pmpErr, upnpErr)
}

// mapNATPMP follows RFC 6886: one request per protocol, retried a few times.
// A home router must forward the same port, the one the torrent client listens on.
func mapNATPMP(ctx context.Context, gw Gateway, port int, lifetime time.Duration) error {
	for _, proto := range []Proto{UDP, TCP} {
		m, err := MapPMP(ctx, gw, proto, port, port, lifetime)
		if err != nil {
			return err
		}
		if m.Public != port {
			return fmt.Errorf("router mapped external port %d instead", m.Public)
		}
	}
	return nil
}

// Proto is a NAT-PMP mapping's protocol, as its request opcode.
type Proto byte

const (
	UDP Proto = 1
	TCP Proto = 2
)

func (p Proto) String() string {
	if p == TCP {
		return "TCP"
	}
	return "UDP"
}

// Mapping is what a NAT-PMP gateway granted.
type Mapping struct {
	Internal int // as the gateway states it; 0 if it says none
	Public   int
	Lifetime time.Duration // map again before it runs out
}

// MapPMP asks gw for one NAT-PMP mapping and nothing else (no UPnP), accepting
// whatever public port it grants. internal 0 lets the gateway choose, as VPN
// servers that forward ports this way expect; publicHint is the port asked for.
func MapPMP(ctx context.Context, gw Gateway, proto Proto, internal, publicHint int, lifetime time.Duration) (Mapping, error) {
	req := make([]byte, 12)
	req[1] = byte(proto)
	binary.BigEndian.PutUint16(req[4:], uint16(internal))
	binary.BigEndian.PutUint16(req[6:], uint16(publicHint))
	binary.BigEndian.PutUint32(req[8:], uint32(lifetime/time.Second))
	resp, err := pmpExchange(ctx, gw, req)
	if err != nil {
		return Mapping{}, err
	}
	return Mapping{
		Internal: int(binary.BigEndian.Uint16(resp[8:])),
		Public:   int(binary.BigEndian.Uint16(resp[10:])),
		Lifetime: time.Duration(binary.BigEndian.Uint32(resp[12:])) * time.Second,
	}, nil
}

// ExternalAddress asks gw for its public address over NAT-PMP: whether it
// speaks NAT-PMP at all.
func ExternalAddress(ctx context.Context, gw Gateway) (net.IP, error) {
	resp, err := pmpExchange(ctx, gw, []byte{0, 0})
	if err != nil {
		return nil, err
	}
	if len(resp) < 12 {
		return nil, errors.New("short NAT-PMP answer")
	}
	return net.IPv4(resp[8], resp[9], resp[10], resp[11]), nil
}

// pmpExchange sends req to gw's NAT-PMP port from a socket of its own and
// returns the matching successful answer.
func pmpExchange(ctx context.Context, gw Gateway, req []byte) ([]byte, error) {
	if gw.pmpPort == 0 {
		gw.pmpPort = 5351
	}
	pc, err := gw.Listen()
	if err != nil {
		return nil, err
	}
	defer pc.Close()
	return pmpRequest(ctx, pc, &net.UDPAddr{IP: gw.IP, Port: gw.pmpPort}, req)
}

func pmpRequest(ctx context.Context, pc net.PacketConn, to net.Addr, req []byte) ([]byte, error) {
	want := 16 // a mapping's answer; an address answer is 12
	if req[1] == 0 {
		want = 12
	}
	buf := make([]byte, 64)
	wait := 250 * time.Millisecond
	for range 3 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if _, err := pc.WriteTo(req, to); err != nil {
			return nil, err
		}
		pc.SetReadDeadline(time.Now().Add(wait))
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				break // timed out, or the router refused the packet: try again
			}
			if n < want || buf[0] != 0 || buf[1] != 128+req[1] {
				continue
			}
			if code := binary.BigEndian.Uint16(buf[2:]); code != 0 {
				return nil, fmt.Errorf("router refused the request (result %d)", code)
			}
			return append([]byte(nil), buf[:n]...), nil
		}
		wait *= 2
	}
	return nil, errors.New("no answer from the router")
}

func mapUPnP(ctx context.Context, gw Gateway, port int, lifetime time.Duration) error {
	loc, err := discover(ctx, gw)
	if err != nil {
		return err
	}
	ctl, service, err := controlURL(ctx, gw, loc)
	if err != nil {
		return err
	}
	for _, proto := range []string{"TCP", "UDP"} {
		if err := addPortMapping(ctx, gw, ctl, service, proto, port, lifetime); err != nil {
			return err
		}
	}
	return nil
}

// discover finds the router's UPnP description with an SSDP search, sent both
// to the multicast group and straight to the router.
func discover(ctx context.Context, gw Gateway) (string, error) {
	pc, err := gw.Listen()
	if err != nil {
		return "", err
	}
	defer pc.Close()
	msg := []byte("M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\n" +
		"ST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\n\r\n")
	for _, ip := range []net.IP{gw.IP, net.IPv4(239, 255, 255, 250)} {
		pc.WriteTo(msg, &net.UDPAddr{IP: ip, Port: gw.ssdpPort})
	}
	deadline := time.Now().Add(3 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	pc.SetReadDeadline(deadline)
	buf := make([]byte, 2048)
	var other string
	for {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			if other != "" {
				return other, nil
			}
			return "", errors.New("no UPnP router answered")
		}
		resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(buf[:n])), nil)
		if err != nil {
			continue
		}
		loc := resp.Header.Get("Location")
		u, err := url.Parse(loc)
		if err != nil || loc == "" {
			continue
		}
		// Another device on the network may answer too; prefer this interface's router.
		if net.ParseIP(u.Hostname()).Equal(gw.IP) {
			return loc, nil
		}
		if other == "" {
			other = loc
		}
	}
}

// controlURL reads the router's description for its WAN connection service.
func controlURL(ctx context.Context, gw Gateway, loc string) (ctl, service string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loc, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := gw.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	dec := xml.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	var inService bool
	var field, typ, path string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			field = tok.Name.Local
			if field == "service" {
				inService, typ, path = true, "", ""
			}
		case xml.CharData:
			if inService && field == "serviceType" {
				typ += strings.TrimSpace(string(tok))
			} else if inService && field == "controlURL" {
				path += strings.TrimSpace(string(tok))
			}
		case xml.EndElement:
			field = ""
			if tok.Name.Local == "service" {
				inService = false
				if strings.Contains(typ, ":WANIPConnection:") || strings.Contains(typ, ":WANPPPConnection:") {
					base, _ := url.Parse(loc)
					rel, err := url.Parse(path)
					if err != nil {
						return "", "", err
					}
					return base.ResolveReference(rel).String(), typ, nil
				}
			}
		}
	}
	return "", "", errors.New("the UPnP router offers no WAN connection service")
}

func addPortMapping(ctx context.Context, gw Gateway, ctl, service, proto string, port int, lifetime time.Duration) error {
	p := strconv.Itoa(port)
	body := `<?xml version="1.0"?>` +
		`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>` +
		`<u:AddPortMapping xmlns:u="` + service + `">` +
		`<NewRemoteHost></NewRemoteHost><NewExternalPort>` + p + `</NewExternalPort><NewProtocol>` + proto + `</NewProtocol>` +
		`<NewInternalPort>` + p + `</NewInternalPort><NewInternalClient>` + gw.Local.String() + `</NewInternalClient>` +
		`<NewEnabled>1</NewEnabled><NewPortMappingDescription>DMS2</NewPortMappingDescription>` +
		`<NewLeaseDuration>` + strconv.Itoa(int(lifetime/time.Second)) + `</NewLeaseDuration>` +
		`</u:AddPortMapping></s:Body></s:Envelope>`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ctl, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", `"`+service+`#AddPortMapping"`)
	resp, err := gw.HTTP.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("router refused the %s mapping: %s", proto, resp.Status)
	}
	return nil
}
