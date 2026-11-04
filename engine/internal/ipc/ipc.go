// Package ipc is the engine's local API: newline-delimited JSON over a named
// pipe that only the current Windows user can open. Each user has a pipe of
// their own, and clients only talk to a pipe served by their own user.
package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"downloadmanagersquared/engine/internal/winutil"
)

// pipePrefix is followed by the user's SID; tests use a pipe of their own.
var pipePrefix = `\\.\pipe\downloadmanagersquared-`

// PipeName is the current user's engine pipe.
func PipeName() (string, error) {
	sid, err := winutil.CurrentUserSID()
	if err != nil {
		return "", err
	}
	return pipePrefix + sid, nil
}

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
	return c.WriteRaw(append(b, '\n'))
}

// WriteRaw sends an already encoded line, e.g. from EncodeEvent.
func (c *Conn) WriteRaw(line []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := c.c.Write(line)
	return err
}

// NewConn wraps an accepted connection; Serve does this for each client, and
// tests for one end of a net.Pipe.
func NewConn(c net.Conn) *Conn { return &Conn{c: c} }

// Emit pushes an event to this client.
func (c *Conn) Emit(name string, data any) error { return c.send(event{name, data}) }

// EncodeEvent encodes an event once so it can be written to many clients.
func EncodeEvent(name string, data any) ([]byte, error) {
	b, err := json.Marshal(event{name, data})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

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
	return winio.ListenPipe(pipePrefix+sid, &winio.PipeConfig{
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
		go serveConn(NewConn(c), h)
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

// Dial connects to the current user's running engine. It refuses a pipe whose
// server runs as anyone else, so another user can't pose as the engine.
func Dial(timeout time.Duration) (net.Conn, error) {
	sid, err := winutil.CurrentUserSID()
	if err != nil {
		return nil, err
	}
	c, err := winio.DialPipe(pipePrefix+sid, &timeout)
	if err != nil {
		return nil, err
	}
	owner, err := serverUser(c)
	if err == nil && owner != sid {
		err = fmt.Errorf("its process runs as %s", owner)
	}
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("not talking to the engine's pipe: %w", err)
	}
	return c, nil
}

// Call sends one request to the current user's running engine and returns its
// result, skipping the events the engine pushes meanwhile.
func Call(method string) (json.RawMessage, error) {
	c, err := Dial(2 * time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	b, _ := json.Marshal(Request{ID: 1, Method: method})
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var resp struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		if json.Unmarshal(line, &resp) != nil || resp.ID != 1 {
			continue // an event
		}
		if resp.Error != "" {
			return nil, errors.New(resp.Error)
		}
		return resp.Result, nil
	}
}

// serverUser is the user of the process serving the pipe c is connected to.
var serverUser = func(c net.Conn) (string, error) {
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return "", errors.New("no pipe handle")
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(f.Fd()), &pid); err != nil {
		return "", err
	}
	return winutil.ProcessUser(pid)
}
