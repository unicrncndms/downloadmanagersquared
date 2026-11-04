package window

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/ipc"
)

// fakeEngine answers every request with its method's name and tells the test
// who connects and hangs up.
type fakeEngine struct {
	connected    chan *ipc.Conn
	disconnected chan struct{}
}

func (f *fakeEngine) Connected(c *ipc.Conn)  { f.connected <- c }
func (f *fakeEngine) Disconnected(*ipc.Conn) { close(f.disconnected) }
func (f *fakeEngine) Call(_ *ipc.Conn, method string, _ json.RawMessage) (any, error) {
	return method, nil
}

func TestTheBridgeCarriesLinesBothWays(t *testing.T) {
	f := &fakeEngine{connected: make(chan *ipc.Conn, 1), disconnected: make(chan struct{})}
	ours, theirs := net.Pipe()
	go ipc.ServeConn(theirs, f)
	got := make(chan string, 8)
	b := newBridge(ours, func(line string) { got <- line })
	c := <-f.connected

	b.send(`{"id":7,"method":"version"}`)
	b.send("{\"id\":8,\n\"method\":\"quit\"}") // two lines: dropped
	if line := next(t, got); line != `{"id":7,"result":"version"}` {
		t.Fatalf("the page got %s", line)
	}
	c.Emit("state", map[string]int{"active": 1})
	if line := next(t, got); line != `{"event":"state","data":{"active":1}}` {
		t.Fatalf("the page got %s", line)
	}
	select {
	case line := <-got:
		t.Fatalf("the page got %s, from a request that should have been dropped", line)
	case <-time.After(200 * time.Millisecond):
	}

	b.close()
	select {
	case <-f.disconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("the engine didn't see the window hang up")
	}
	b.send(`{"id":9,"method":"version"}`) // after close: neither blocks nor panics
}

func next(t *testing.T, got chan string) string {
	t.Helper()
	select {
	case line := <-got:
		return line
	case <-time.After(2 * time.Second):
		t.Fatal("nothing reached the page")
		return ""
	}
}
