package winutil

import (
	"encoding/binary"
	"errors"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

var pGetExtendedTcpTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

const tcpTableOwnerPIDConnections = 4

// tcpRow is MIB_TCPROW_OWNER_PID: addresses and ports in network byte order.
type tcpRow struct {
	State, LocalAddr, LocalPort, RemoteAddr, RemotePort, PID uint32
}

// TCPOwnerPID is the process holding the IPv4 TCP connection from local to remote.
func TCPOwnerPID(local, remote *net.TCPAddr) (uint32, error) {
	var buf []byte
	size := uint32(16 << 10)
	for range 5 {
		buf = make([]byte, size)
		r, _, _ := pGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0,
			windows.AF_INET, tcpTableOwnerPIDConnections, 0)
		if r == 0 {
			break
		}
		if windows.Errno(r) != windows.ERROR_INSUFFICIENT_BUFFER {
			return 0, windows.Errno(r)
		}
		buf = nil
	}
	if buf == nil {
		return 0, errors.New("the TCP table keeps growing")
	}
	n := binary.LittleEndian.Uint32(buf)
	rows := unsafe.Slice((*tcpRow)(unsafe.Pointer(&buf[4])), n)
	for _, r := range rows {
		if sameEndpoint(r.LocalAddr, r.LocalPort, local) && sameEndpoint(r.RemoteAddr, r.RemotePort, remote) {
			return r.PID, nil
		}
	}
	return 0, errors.New("connection not in the TCP table")
}

func sameEndpoint(addr, port uint32, a *net.TCPAddr) bool {
	var ip [4]byte
	binary.LittleEndian.PutUint32(ip[:], addr)
	p := uint16(port)<<8 | uint16(port)>>8
	return a != nil && net.IP(ip[:]).Equal(a.IP) && int(p) == a.Port
}
