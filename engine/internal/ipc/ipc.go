// Package ipc is the engine's local API: newline-delimited JSON over a named
// pipe that only the current Windows user can open.
package ipc

import (
	"bufio"
	"encoding/json"
	"net"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"

	"downloadmanagersquared/engine/internal/winutil"
)

const PipeName = `\\.\pipe\downloadmanagersquared`

// Request is a call from a client: {"id":1,"method":"add","params":{...}}.
type Request struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type response struct {
	ID     int64  `json:"id"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

type event struct {
	Event string `json:"event"`
	Data  any    `json:"data,omitempty"`
}

type Conn struct {
	c   net.Conn
	wmu sync.Mutex
}

func (c *Conn) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err = c.c.Write(append(b, '\n'))
	return err
}

// Emit pushes an event to this client.
func (c *Conn) Emit(name string, data any) error { return c.send(event{name, data}) }

type Handler interface {
	Connected(c *Conn)
	Disconnected(c *Conn)
	Call(c *Conn, method string, params json.RawMessage) (any, error)
}

// Listen opens the pipe. It fails if another engine already owns it.
func Listen() (net.Listener, error) {
	sid, err := winutil.CurrentUserSID()
	if err != nil {
		return nil, err
	}
	return winio.ListenPipe(PipeName, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;" + sid + ")",
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
}

func Serve(ln net.Listener, h Handler) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go serveConn(&Conn{c: c}, h)
	}
}

func serveConn(c *Conn, h Handler) {
	defer c.c.Close()
	h.Connected(c)
	defer h.Disconnected(c)
	r := bufio.NewReaderSize(c.c, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req Request
		if json.Unmarshal(line, &req) != nil {
			continue
		}
		go func() {
			res, err := h.Call(c, req.Method, req.Params)
			resp := response{ID: req.ID, Result: res}
			if err != nil {
				resp.Error = err.Error()
			}
			c.send(resp)
		}()
	}
}

// Dial connects to a running engine.
func Dial(timeout time.Duration) (net.Conn, error) {
	return winio.DialPipe(PipeName, &timeout)
}
