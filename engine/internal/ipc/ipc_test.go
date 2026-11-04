package ipc

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/winutil"
)

// testPipe gives the test a pipe of its own, so a running engine isn't touched.
func testPipe(t *testing.T) {
	t.Helper()
	old := pipePrefix
	pipePrefix = fmt.Sprintf(`\\.\pipe\dms-test-%d-`, time.Now().UnixNano())
	t.Cleanup(func() { pipePrefix = old })
}

func TestPipeNameHoldsTheUsersSID(t *testing.T) {
	sid, err := winutil.CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	name, err := PipeName()
	if err != nil {
		t.Fatal(err)
	}
	if name != `\\.\pipe\downloadmanagersquared-`+sid {
		t.Fatalf("pipe name %q", name)
	}
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("hi\n"))
			c.Close()
		}
	}()
	return ln
}

// eventsFirst answers each call after pushing an event, as the engine does
// when something changes between a request and its response.
type eventsFirst struct{}

func (eventsFirst) Connected(*Conn)    {}
func (eventsFirst) Disconnected(*Conn) {}
func (eventsFirst) Call(c *Conn, method string, _ json.RawMessage) (any, error) {
	c.Emit("progress", map[string]int{"id": 1})
	if method == "fail" {
		return nil, errors.New("no such method")
	}
	return map[string]string{"got": method}, nil
}

func TestCallReturnsTheResultPastEvents(t *testing.T) {
	testPipe(t)
	ln, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go Serve(ln, eventsFirst{})

	res, err := Call("quit")
	if err != nil {
		t.Fatal(err)
	}
	if string(res) != `{"got":"quit"}` {
		t.Fatalf("result %s", res)
	}
	if _, err := Call("fail"); err == nil || err.Error() != "no such method" {
		t.Fatalf("error %v", err)
	}
}

func TestDialReachesAnEngineOfTheSameUser(t *testing.T) {
	testPipe(t)
	listen(t)
	c, err := Dial(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestDialRefusesAServerOfAnotherUser(t *testing.T) {
	testPipe(t)
	listen(t)
	old := serverUser
	t.Cleanup(func() { serverUser = old })
	for _, fake := range []func(net.Conn) (string, error){
		func(net.Conn) (string, error) { return "S-1-5-21-1-2-3-1001", nil },
		func(net.Conn) (string, error) { return "", errors.New("access denied") },
	} {
		serverUser = fake
		if c, err := Dial(time.Second); err == nil {
			c.Close()
			t.Fatal("dialled a pipe another user's process serves")
		} else if !strings.Contains(err.Error(), "engine") {
			t.Fatalf("error %q doesn't say what it was about", err)
		}
	}
}

func TestListenFailsWhileTheNameIsTaken(t *testing.T) {
	testPipe(t)
	listen(t)
	if ln, err := Listen(); err == nil {
		ln.Close()
		t.Fatal("two engines listen on one pipe")
	}
}
