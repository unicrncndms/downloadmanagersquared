package ipc

import (
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
