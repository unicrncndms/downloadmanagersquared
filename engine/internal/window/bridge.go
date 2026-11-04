package window

import (
	"bufio"
	"io"
	"net"
	"strings"
	"sync"
)

// bridge is the Go half of the window's pipe: it writes the page's request
// lines to the engine over conn, and hands every line the engine writes back
// to deliver, in order. Request ids, timeouts and events are the page's
// business (the window adapter in host.js).
type bridge struct {
	conn net.Conn
	out  chan string
	done chan struct{}
	once sync.Once
}

func newBridge(conn net.Conn, deliver func(line string)) *bridge {
	b := &bridge{conn: conn, out: make(chan string, 256), done: make(chan struct{})}
	go func() { // the page's thread never waits on the engine
		for {
			select {
			case line := <-b.out:
				if _, err := io.WriteString(conn, line+"\n"); err != nil {
					return
				}
			case <-b.done:
				return
			}
		}
	}()
	go func() {
		r := bufio.NewReaderSize(conn, 64<<10)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			deliver(strings.TrimRight(line, "\r\n"))
		}
	}()
	return b
}

// send queues one request line from the page. A line break would split it in
// two requests, so such a line is dropped.
func (b *bridge) send(line string) {
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return
	}
	select {
	case b.out <- line:
	case <-b.done:
	}
}

// close hangs up: the engine forgets this client.
func (b *bridge) close() {
	b.once.Do(func() {
		close(b.done)
		b.conn.Close()
	})
}
