// Package ipc is the engine's local API: newline-delimited JSON over a named
// pipe that only the current Windows user can open. Each user has a pipe of
// their own, and clients only talk to a pipe served by their own user.
package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"regexp"
	"runtime/debug"
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

// Conn is one client. Everything for it is written by one goroutine of its
// own, so a client that doesn't read holds up only itself: writes here just
// queue. Events and answers keep their order; the "latest" picture (the
// state, the badge) is one slot that a newer one replaces.
type Conn struct {
	c      net.Conn
	mu     sync.Mutex // guards the queue
	q      []queued
	latest []byte
	wake   chan struct{}
	done   chan struct{}
	once   sync.Once
}

type queued struct {
	b    []byte
	sync chan struct{} // closed when the writer reaches it (Sync)
}

// maxQueued is how many lines may wait for a client before it is hung up on.
const maxQueued = 4096

func (c *Conn) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.WriteRaw(append(b, '\n'))
}

// WriteRaw queues an already encoded line, e.g. from EncodeEvent.
func (c *Conn) WriteRaw(line []byte) error { return c.enqueue(queued{b: line}) }

func (c *Conn) enqueue(q queued) error {
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return net.ErrClosed
	default:
	}
	if len(c.q) >= maxQueued {
		c.mu.Unlock()
		log.Printf("dropping a client that doesn't take events")
		c.Close()
		return net.ErrClosed
	}
	c.q = append(c.q, q)
	c.mu.Unlock()
	c.wakeUp()
	return nil
}

// WriteLatest queues a picture of the current state: one not yet written when
// the next comes is dropped.
func (c *Conn) WriteLatest(line []byte) error {
	c.mu.Lock()
	c.latest = line
	c.mu.Unlock()
	c.wakeUp()
	return nil
}

// Sync waits until what is queued now has been written, or d is over.
func (c *Conn) Sync(d time.Duration) {
	s := make(chan struct{})
	if c.enqueue(queued{sync: s}) != nil {
		return
	}
	select {
	case <-s:
	case <-c.done:
	case <-time.After(d):
	}
}

func (c *Conn) wakeUp() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Conn) writer() {
	for {
		select {
		case <-c.wake:
		case <-c.done:
			return
		}
		c.mu.Lock()
		q, latest := c.q, c.latest
		c.q, c.latest = nil, nil
		c.mu.Unlock()
		for _, it := range q {
			if it.sync != nil {
				close(it.sync)
			} else if !c.write(it.b) {
				return
			}
		}
		if latest != nil && !c.write(latest) {
			return
		}
	}
}

// write hangs up on a client that takes nothing for 10 s; it connects again
// when it can.
func (c *Conn) write(b []byte) bool {
	c.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.c.Write(b); err != nil {
		c.Close()
		return false
	}
	return true
}

// Close hangs up on the client: its serving ends as if it had left.
func (c *Conn) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.c.Close()
}

// NewConn wraps an accepted connection; Serve does this for each client, and
// tests for one end of a net.Pipe.
func NewConn(c net.Conn) *Conn {
	cn := &Conn{c: c, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go cn.writer()
	return cn
}

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

// Serve takes clients until the listener is closed. Another error is logged
// and tried again; a listener that fails this many times in a row is given up on.
func Serve(ln net.Listener, h Handler) {
	failed := 0
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("ipc: accepting a client: %v", err)
			if failed++; failed >= maxAcceptFailures {
				log.Printf("ipc: giving up on the pipe")
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		failed = 0
		go serveConn(NewConn(c), h)
	}
}

const maxAcceptFailures = 20

// ServeConn serves one client already connected, e.g. the far end of the
// Downloads window's in-memory pipe, the same as one on the named pipe. It
// returns when the client hangs up.
func ServeConn(c net.Conn, h Handler) { serveConn(NewConn(c), h) }

func serveConn(c *Conn, h Handler) {
	defer c.Close()
	h.Connected(c)
	defer h.Disconnected(c)
	r := bufio.NewReaderSize(c.c, 64<<10)
	inflight := make(chan struct{}, maxInflight)
	for {
		line, err := ReadLine(r, MaxMessage)
		if errors.Is(err, ErrTooLong) {
			// Answered, so the caller doesn't wait for its id forever.
			log.Printf("ipc: refused a message over %d bytes", MaxMessage)
			if id := LineID(line); id != nil {
				c.WriteRaw(append(TooLongReply(id), '\n'))
			}
			continue
		}
		if err != nil {
			return
		}
		var req Request
		if json.Unmarshal(line, &req) != nil {
			continue
		}
		inflight <- struct{}{} // a flood waits here instead of starting a goroutine per line
		go func() {
			defer func() { <-inflight }()
			resp := response{ID: req.ID}
			defer func() { c.send(resp) }()
			defer func() {
				if p := recover(); p != nil {
					log.Printf("ipc: %s panicked: %v\n%s", req.Method, p, debug.Stack())
					resp = response{ID: req.ID, Error: "internal error"}
				}
			}()
			start := time.Now()
			res, err := h.Call(c, req.Method, req.Params)
			if d := time.Since(start); d > slowCall {
				// The pages give up on a call after a minute ("engine did not answer"): this names it.
				log.Printf("ipc: %s took %s", req.Method, d.Round(time.Second))
			}
			resp.Result = res
			if err != nil {
				resp.Error = err.Error()
			}
		}()
	}
}

// MaxMessage is the longest line the engine reads from a client. Real
// requests are a few kilobytes; this only stops a runaway client from growing
// the engine's memory without bound.
const MaxMessage = 4 << 20

// slowCall is how long a call may take before the log says so.
const slowCall = 10 * time.Second

// maxInflight is how many calls one client may have running at once.
const maxInflight = 64

// ErrTooLong is ReadLine's answer to a line over its limit. The line has been
// read past, so the next one can follow; the bytes returned with it are the
// line's start, enough for LineID.
var ErrTooLong = errors.New("line too long")

const headLen = 256

var idPrefix = regexp.MustCompile(`^\s*\{\s*"id"\s*:\s*(-?\d+)`)

// LineID is the id of a request whose line starts {"id":N, as the extension
// writes it; nil if the start has none.
func LineID(head []byte) json.RawMessage {
	if m := idPrefix.FindSubmatch(head); m != nil {
		return json.RawMessage(m[1])
	}
	return nil
}

// TooLongReply is the error reply to the request with this id.
func TooLongReply(id json.RawMessage) []byte {
	return []byte(`{"id":` + string(id) + `,"error":"message too long"}`)
}

// ReadLine reads one newline-terminated line of at most max bytes, newline
// included. A longer line is skipped up to its newline and gives ErrTooLong
// with its first bytes.
func ReadLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	tooLong := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > max {
				tooLong = true
				line = append(line, chunk...)
				if len(line) > headLen {
					line = line[:headLen]
				}
			} else {
				line = append(line, chunk...)
			}
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err != nil:
			return nil, err
		case tooLong:
			return line, ErrTooLong
		default:
			return line, nil
		}
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
