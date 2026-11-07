package netif

import (
	"context"
	"encoding/binary"
	"net"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestSameRouterGoesByGatewayMACNotGatewayIP(t *testing.T) {
	gw := net.IPv4(192, 168, 1, 1)
	got := SameRouter([]Interface{
		{ID: "eth", Gateway: gw, GatewayMAC: "aa:aa:aa:aa:aa:aa"},
		{ID: "wifi", Gateway: gw, GatewayMAC: "aa:aa:aa:aa:aa:aa"},
		{ID: "usb", Gateway: gw, GatewayMAC: "bb:bb:bb:bb:bb:bb"}, // another router, same IP
		{ID: "ppp", Gateway: net.IPv4(10, 0, 0, 1)},               // no MAC known
		{ID: "lte", Gateway: net.IPv4(10, 0, 0, 1)},
	})
	for _, ids := range got {
		sort.Strings(ids)
	}
	want := map[string][]string{"eth": {"wifi"}, "wifi": {"eth"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SameRouter = %v, want %v", got, want)
	}
}

// stunServer answers binding requests with XOR-MAPPED-ADDRESS public, after
// dropping the first drop requests.
func stunServer(t *testing.T, public net.IP, drop int) net.Addr {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 20 || drop > 0 {
				drop--
				continue
			}
			resp := make([]byte, 20+4+8)
			binary.BigEndian.PutUint16(resp[0:], stunBindSuccess)
			binary.BigEndian.PutUint16(resp[2:], 12)
			copy(resp[4:20], buf[4:20]) // cookie and transaction id
			// An unknown attribute first, padded, then the address.
			resp = append(resp[:20], 0x80, 0x22, 0, 3, 'd', 'm', 's', 0)
			binary.BigEndian.PutUint16(resp[2:], 8+12)
			x := []byte{0, 0x20, 0, 8, 0, 0x01, 0, 0, 0, 0, 0, 0}
			var m [4]byte
			binary.BigEndian.PutUint32(m[:], stunMagic)
			for i, b := range public.To4() {
				x[8+i] = b ^ m[i]
			}
			resp = append(resp, x...)
			pc.WriteTo(resp, from)
		}
	}()
	return pc.LocalAddr()
}

func TestStunQueryReadsTheMappedAddressAndResends(t *testing.T) {
	public := net.IPv4(203, 0, 113, 7)
	server := stunServer(t, public, 1)
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ip, err := stunQuery(ctx, pc, server)
	if err != nil {
		t.Fatal(err)
	}
	if !ip.Equal(public) {
		t.Fatalf("public IP %v, want %v", ip, public)
	}
}

func TestStunAnswerToAnotherRequestIsIgnored(t *testing.T) {
	_, txid := stunRequest()
	resp := make([]byte, 20)
	binary.BigEndian.PutUint16(resp[0:], stunBindSuccess)
	binary.BigEndian.PutUint32(resp[4:], stunMagic)
	if _, err := parseStunResponse(resp, txid); err == nil {
		t.Fatal("an answer with another transaction id was taken")
	}
}
