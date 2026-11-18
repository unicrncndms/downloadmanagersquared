package cdp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/winutil"
)

// fake is the browser's end of the pipe: it reads calls and writes what the test says.
type fake struct {
	calls chan wire
	out   *io.PipeWriter
	in    *io.PipeReader
	wmu   sync.Mutex
}

func newFake(t *testing.T, onEvent func(Event)) (*Browser, *fake) {
	t.Helper()
	toR, toW := io.Pipe()     // engine -> browser
	fromR, fromW := io.Pipe() // browser -> engine
	f := &fake{calls: make(chan wire, 16), out: fromW, in: toR}
	go func() {
		br := bufio.NewReader(toR)
		for {
			line, err := br.ReadBytes(0)
			if err != nil {
				close(f.calls)
				return
			}
			var m wire
			json.Unmarshal(line[:len(line)-1], &m)
			f.calls <- m
		}
	}()
	b := newBrowser(fromR, toW, onEvent)
	t.Cleanup(b.Close)
	return b, f
}

func (f *fake) send(v any) {
	data, _ := json.Marshal(v)
	f.wmu.Lock()
	f.out.Write(append(data, 0))
	f.wmu.Unlock()
}

func (f *fake) next(t *testing.T) wire {
	t.Helper()
	select {
	case m := <-f.calls:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("no call reached the browser")
		return wire{}
	}
}

func TestRepliesReachTheirCallsWhateverTheOrder(t *testing.T) {
	b, f := newFake(t, nil)
	type res struct {
		got string
		err error
	}
	results := make(chan res, 2)
	for _, m := range []string{"A.one", "A.two"} {
		go func() {
			r, err := b.Call(context.Background(), "s1", m, map[string]int{"x": 1})
			results <- res{string(r), err}
		}()
	}
	c1, c2 := f.next(t), f.next(t)
	if c1.SessionID != "s1" || c1.ID == c2.ID {
		t.Fatalf("calls %+v %+v", c1, c2)
	}
	// Answer the second first.
	f.send(map[string]any{"id": c2.ID, "result": map[string]string{"m": c2.Method}})
	f.send(map[string]any{"id": c1.ID, "result": map[string]string{"m": c1.Method}})
	seen := map[string]bool{}
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		seen[r.got] = true
	}
	if !seen[`{"m":"A.one"}`] || !seen[`{"m":"A.two"}`] {
		t.Fatalf("replies went to the wrong calls: %v", seen)
	}
}

func TestAProtocolErrorIsTheCallsError(t *testing.T) {
	b, f := newFake(t, nil)
	go func() {
		c := <-f.calls
		f.send(map[string]any{"id": c.ID, "error": map[string]string{"message": "No target with given id"}})
	}()
	_, err := b.Call(context.Background(), "", "Target.closeTarget", nil)
	if err == nil || err.Error() != "Target.closeTarget: No target with given id" || errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v", err)
	}
}

func TestEventsArriveInOrderAndMayCall(t *testing.T) {
	var got []string
	done := make(chan struct{})
	var b *Browser
	b, f := newFake(t, func(e Event) {
		got = append(got, e.Method+"@"+e.SessionID)
		if e.Method == "Target.attachedToTarget" {
			// A handler that calls must not block the reply it waits for.
			var p struct{ SessionID string }
			json.Unmarshal(e.Params, &p)
			if _, err := b.Call(context.Background(), p.SessionID, "Fetch.enable", nil); err != nil {
				t.Error(err)
			}
		}
		if len(got) == 3 {
			close(done)
		}
	})
	f.send(map[string]any{"method": "Target.attachedToTarget", "params": map[string]string{"sessionId": "s9"}, "sessionId": ""})
	f.send(map[string]any{"method": "Fetch.requestPaused", "sessionId": "s9"})
	f.send(map[string]any{"method": "Page.loadEventFired", "sessionId": "s9"})
	c := f.next(t)
	if c.Method != "Fetch.enable" || c.SessionID != "s9" {
		t.Fatalf("call %+v", c)
	}
	f.send(map[string]any{"id": c.ID, "result": map[string]any{}})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("events so far: %v", got)
	}
	want := []string{"Target.attachedToTarget@", "Fetch.requestPaused@s9", "Page.loadEventFired@s9"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events %v, want %v", got, want)
		}
	}
}

func TestADeadBrowserFailsEveryPendingCallAndLaterOnes(t *testing.T) {
	b, f := newFake(t, nil)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := b.Call(context.Background(), "", "Page.navigate", nil)
			errs <- err
		}()
	}
	f.next(t)
	f.next(t)
	f.out.Close() // the browser exits
	for range 2 {
		if err := <-errs; !errors.Is(err, ErrClosed) {
			t.Fatalf("pending call: %v", err)
		}
	}
	<-b.Done()
	if !errors.Is(b.Err(), ErrClosed) {
		t.Fatalf("Err = %v", b.Err())
	}
	if _, err := b.Call(context.Background(), "", "Page.reload", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("later call: %v", err)
	}
}

func TestACallGivesUpWithItsContext(t *testing.T) {
	b, f := newFake(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	go func() { <-f.calls }()
	if _, err := b.Call(ctx, "", "Runtime.evaluate", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestExePrefersChromeThenEdge(t *testing.T) {
	ff := winutil.Browser{Name: "Firefox", Exe: `C:\ff\firefox.exe`}
	edge := winutil.Browser{Name: "Edge", Exe: `C:\e\msedge.exe`, Chromium: true}
	chrome := winutil.Browser{Name: "Chrome", Exe: `C:\c\chrome.exe`, Chromium: true}
	brave := winutil.Browser{Name: "Brave", Exe: `C:\b\brave.exe`, Chromium: true}
	for _, c := range []struct {
		in   []winutil.Browser
		want string
	}{
		{[]winutil.Browser{ff, brave, edge, chrome}, chrome.Exe},
		{[]winutil.Browser{ff, brave, edge}, edge.Exe},
		{[]winutil.Browser{ff, brave}, brave.Exe},
		{[]winutil.Browser{ff}, ""},
	} {
		if got := Exe(c.in); got != c.want {
			t.Errorf("Exe(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
