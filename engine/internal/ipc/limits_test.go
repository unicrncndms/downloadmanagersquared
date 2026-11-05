package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"testing"
	"time"
)

// echo answers each call with its method; "boom" panics, as a handler might on
// params it didn't expect.
type echo struct{}

func (echo) Connected(*Conn)    {}
func (echo) Disconnected(*Conn) {}
func (echo) Call(_ *Conn, method string, _ json.RawMessage) (any, error) {
	if method == "boom" {
		panic("boom")
	}
	return method, nil
}

// served is a client end of a connection ServeConn serves with echo.
func served(t *testing.T) (net.Conn, *bufio.Reader) {
	t.Helper()
	client, server := net.Pipe()
	go ServeConn(server, echo{})
	t.Cleanup(func() { client.Close() })
	client.SetDeadline(time.Now().Add(10 * time.Second))
	return client, bufio.NewReader(client)
}

func answer(t *testing.T, r *bufio.Reader) response {
	t.Helper()
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("%v: %s", err, line)
	}
	return resp
}

func TestAnOversizedLineIsDroppedAndTheConnectionCarriesOn(t *testing.T) {
	c, r := served(t)
	go func() {
		huge := bytes.Repeat([]byte("x"), MaxMessage+1)
		c.Write(append([]byte(`{"id":1,"method":"`), huge...))
		c.Write([]byte("\"}\n" + `{"id":2,"method":"ok"}` + "\n"))
	}()
	if resp := answer(t, r); resp.ID != 2 || resp.Result != "ok" {
		t.Fatalf("got %+v, want only the request after the oversized one answered", resp)
	}
}

func TestMalformedLinesAreSkipped(t *testing.T) {
	c, r := served(t)
	go c.Write([]byte("not json\n{\"id\":\"one\"}\n\n[1,2]\n{\"id\":3,\"method\":\"ok\"}\n"))
	if resp := answer(t, r); resp.ID != 3 {
		t.Fatalf("got %+v", resp)
	}
}

func TestAPanickingCallAnswersWithAnErrorAndTheEngineLives(t *testing.T) {
	c, r := served(t)
	go c.Write([]byte(`{"id":1,"method":"boom"}` + "\n"))
	if resp := answer(t, r); resp.ID != 1 || resp.Error == "" {
		t.Fatalf("got %+v, want an error answer", resp)
	}
	go c.Write([]byte(`{"id":2,"method":"ok"}` + "\n"))
	if resp := answer(t, r); resp.ID != 2 || resp.Result != "ok" {
		t.Fatalf("got %+v", resp)
	}
}
