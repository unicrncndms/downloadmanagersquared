// Package cdp starts a Chromium of the engine's own and talks to it over the
// DevTools protocol, through a pipe (no port another process could reach):
// calls, events and flat sessions. The resolver for Chromium downloads drives
// it (ADR 0011).
package cdp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ErrClosed is wrapped by every call that fails because the browser is gone.
var ErrClosed = errors.New("the resolver browser is closed")

// Event is a protocol event. SessionID is empty for the browser's own.
type Event struct {
	Method    string
	SessionID string
	Params    json.RawMessage
}

// Browser is one connection to a browser. Safe for concurrent use.
type Browser struct {
	PID uint32 // the browser's main process, which owns its windows; 0 when not started by Start

	w io.WriteCloser

	mu      sync.Mutex
	next    int64
	pending map[int64]chan reply
	err     error // why it ended; set once, before done closes
	done    chan struct{}

	queue  chan struct{} // wakes the event dispatcher
	events []Event       // waiting for the dispatcher, in arrival order
	wmu    sync.Mutex    // one message at a time on the pipe

	stop func() // ends the process, if there is one
}

type reply struct {
	result json.RawMessage
	err    error
}

type wire struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    any             `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type inbound struct {
	wire
	Params json.RawMessage `json:"params,omitempty"`
}

// newBrowser reads replies and events from r and writes calls to w. onEvent
// gets every event in order on one goroutine of its own, so it may Call; an
// event waits while onEvent runs.
func newBrowser(r io.Reader, w io.WriteCloser, onEvent func(Event)) *Browser {
	b := &Browser{w: w, pending: map[int64]chan reply{}, done: make(chan struct{}), queue: make(chan struct{}, 1), stop: func() {}}
	go b.read(r)
	go b.dispatch(onEvent)
	return b
}

func (b *Browser) read(r io.Reader) {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes(0) // each message ends in a NUL
		if err != nil {
			b.end(fmt.Errorf("%w: %v", ErrClosed, err))
			return
		}
		var m inbound
		if err := json.Unmarshal(line[:len(line)-1], &m); err != nil {
			continue
		}
		if m.ID != 0 {
			b.mu.Lock()
			ch := b.pending[m.ID]
			delete(b.pending, m.ID)
			b.mu.Unlock()
			if ch == nil {
				continue
			}
			if m.Error != nil {
				ch <- reply{err: errors.New(m.Error.Message)}
			} else {
				ch <- reply{result: m.Result}
			}
			continue
		}
		b.mu.Lock()
		b.events = append(b.events, Event{m.Method, m.SessionID, m.Params})
		b.mu.Unlock()
		select {
		case b.queue <- struct{}{}:
		default:
		}
	}
}

func (b *Browser) dispatch(onEvent func(Event)) {
	for {
		select {
		case <-b.queue:
		case <-b.done:
			return
		}
		for {
			b.mu.Lock()
			if len(b.events) == 0 {
				b.mu.Unlock()
				break
			}
			e := b.events[0]
			b.events = b.events[1:]
			b.mu.Unlock()
			if onEvent != nil {
				onEvent(e)
			}
		}
	}
}

// end fails every pending call with err and marks the browser gone.
func (b *Browser) end(err error) {
	b.mu.Lock()
	if b.err != nil {
		b.mu.Unlock()
		return
	}
	b.err = err
	pending := b.pending
	b.pending = nil
	close(b.done)
	b.mu.Unlock()
	for _, ch := range pending {
		ch <- reply{err: err}
	}
	b.w.Close()
}

// Call sends method to the browser (sessionID "") or to an attached target's
// session, and waits for its result.
func (b *Browser) Call(ctx context.Context, sessionID, method string, params any) (json.RawMessage, error) {
	b.mu.Lock()
	if b.err != nil {
		b.mu.Unlock()
		return nil, b.err
	}
	b.next++
	id := b.next
	ch := make(chan reply, 1)
	b.pending[id] = ch
	b.mu.Unlock()
	forget := func() {
		b.mu.Lock()
		delete(b.pending, id) // a nil map after end: delete is a no-op
		b.mu.Unlock()
	}

	msg, err := json.Marshal(wire{ID: id, Method: method, Params: params, SessionID: sessionID})
	if err != nil {
		forget()
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	b.wmu.Lock()
	_, err = b.w.Write(append(msg, 0))
	b.wmu.Unlock()
	if err != nil {
		b.end(fmt.Errorf("%w: %v", ErrClosed, err))
	}
	select {
	case r := <-ch:
		if r.err != nil && !errors.Is(r.err, ErrClosed) {
			return nil, fmt.Errorf("%s: %w", method, r.err)
		}
		return r.result, r.err
	case <-ctx.Done():
		forget()
		return nil, fmt.Errorf("%s: %w", method, ctx.Err())
	}
}

// Done is closed once the browser is gone; Err then says why.
func (b *Browser) Done() <-chan struct{} { return b.done }

// Err is why the browser is gone, nil while it runs.
func (b *Browser) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

// Close ends the connection and the browser's process.
func (b *Browser) Close() {
	b.end(fmt.Errorf("%w: closed by the engine", ErrClosed))
	b.stop()
}
