package resolver

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/cdp"
)

// Which part of the engine's browser a captcha service holds against it
// (ticket 35): a managed Cloudflare Turnstile with a real site key passes a
// normal browser without a click, so each stage opens the demo and checks
// that the widget's token got filled. Stages add the engine browser's parts
// one at a time. Needs a person nowhere, but the internet: set DMS_TURNSTILE=1.
const turnstileDemo = "https://peet.ws/turnstile-test/managed.html"

func TestTurnstileBisect(t *testing.T) {
	exe := os.Getenv("DMS_CDP_EXE")
	if exe == "" || os.Getenv("DMS_TURNSTILE") == "" {
		t.Skip("set DMS_CDP_EXE and DMS_TURNSTILE=1")
	}
	wait := 25 * time.Second
	port := openSocks(t)
	type stage struct {
		name                                  string
		context, proxy, attach, fetch, blocks bool
		pageEnable                            bool
		noIframes, docOnly, otherOnly         bool // Fetch only on page sessions; only one of the two patterns
	}
	base := stage{context: true, proxy: true, attach: true, pageEnable: true}
	with := func(name string, f func(*stage)) stage { s := base; s.name = name; f(&s); return s }
	stages := []stage{
		{name: "pipe only"},
		with("context+proxy+attach+page", func(*stage) {}),
		with("fetch both, all sessions", func(s *stage) { s.fetch = true }),
		with("fetch both, pages only", func(s *stage) { s.fetch, s.noIframes = true, true }),
		with("fetch Document, all", func(s *stage) { s.fetch, s.docOnly = true, true }),
		with("fetch Other, all", func(s *stage) { s.fetch, s.otherOnly = true, true }),
		with("fetch Document, pages only", func(s *stage) { s.fetch, s.docOnly, s.noIframes = true, true, true }),
	}
	for _, st := range stages {
		var b *cdp.Browser
		b, err := cdp.Start(exe, t.TempDir(), func(e cdp.Event) {
			if b == nil {
				return
			}
			switch e.Method {
			case "Target.attachedToTarget":
				var p struct {
					SessionID  string `json:"sessionId"`
					TargetInfo struct {
						Type string `json:"type"`
					} `json:"targetInfo"`
				}
				json.Unmarshal(e.Params, &p)
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					if st.fetch && !(st.noIframes && p.TargetInfo.Type != "page") {
						var pats []any
						if !st.otherOnly {
							pats = append(pats, map[string]any{"resourceType": "Document", "requestStage": "Response"})
						}
						if !st.docOnly {
							pats = append(pats, map[string]any{"resourceType": "Other", "requestStage": "Response"})
						}
						if st.blocks {
							pats = append(pats, map[string]any{"resourceType": "Media", "requestStage": "Request"}, map[string]any{"resourceType": "Font", "requestStage": "Request"})
						}
						b.Call(ctx, p.SessionID, "Fetch.enable", map[string]any{"patterns": pats})
					}
					b.Call(ctx, p.SessionID, "Target.setAutoAttach", map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true})
					if st.pageEnable {
						b.Call(ctx, p.SessionID, "Page.enable", nil)
					}
					b.Call(ctx, p.SessionID, "Runtime.runIfWaitingForDebugger", nil)
				}()
			case "Fetch.requestPaused":
				var p struct {
					RequestID    string `json:"requestId"`
					ResourceType string `json:"resourceType"`
				}
				json.Unmarshal(e.Params, &p)
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					if p.ResourceType == "Media" || p.ResourceType == "Font" {
						b.Call(ctx, e.SessionID, "Fetch.failRequest", map[string]any{"requestId": p.RequestID, "errorReason": "BlockedByClient"})
					} else {
						b.Call(ctx, e.SessionID, "Fetch.continueRequest", map[string]any{"requestId": p.RequestID})
					}
				}()
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		call := func(session, method string, params any) json.RawMessage {
			r, err := b.Call(ctx, session, method, params)
			if err != nil {
				t.Fatalf("%s: %s: %v", st.name, method, err)
			}
			return r
		}
		if st.attach {
			call("", "Target.setAutoAttach", map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true})
		}
		create := map[string]any{"url": turnstileDemo, "newWindow": true}
		if st.context {
			bc := map[string]any{"disposeOnDetach": true}
			if st.proxy {
				bc["proxyServer"] = "socks5://127.0.0.1:" + strconv.Itoa(port)
				bc["proxyBypassList"] = "<-loopback>"
			}
			var c struct{ BrowserContextID string }
			json.Unmarshal(call("", "Target.createBrowserContext", bc), &c)
			create["browserContextId"] = c.BrowserContextID
		}
		var tg struct{ TargetID string }
		json.Unmarshal(call("", "Target.createTarget", create), &tg)
		time.Sleep(wait)
		var s struct{ SessionID string }
		json.Unmarshal(call("", "Target.attachToTarget", map[string]any{"targetId": tg.TargetID, "flatten": true}), &s)
		r := call(s.SessionID, "Runtime.evaluate", map[string]any{"returnByValue": true,
			"expression": `JSON.stringify({token: (document.querySelector("[name=cf-turnstile-response]") || {value: ""}).value.length, frames: document.querySelectorAll("iframe").length, webdriver: navigator.webdriver, title: document.title})`})
		var v struct {
			Result struct{ Value string } `json:"result"`
		}
		json.Unmarshal(r, &v)
		t.Logf("%-28s %s", st.name, v.Result.Value)
		cancel()
		b.Close()
	}
}

// The engine's own Driver, as a resolve opens a page: its context through a
// SOCKS port, the page script in, a record message sent.
func TestTurnstileInTheDriver(t *testing.T) {
	exe := os.Getenv("DMS_CDP_EXE")
	if exe == "" || os.Getenv("DMS_TURNSTILE") == "" {
		t.Skip("set DMS_CDP_EXE and DMS_TURNSTILE=1")
	}
	if dir := os.Getenv("DMS_PAGE_SCRIPT_DIR"); dir != "" {
		was := pageScript
		t.Cleanup(func() { pageScript = was })
		pageScript = func() (string, error) {
			ready, err := os.ReadFile(dir + "/lib/ready.js")
			if err != nil {
				return "", err
			}
			content, err := os.ReadFile(dir + "/resolver-content.js")
			return prelude + string(ready) + "\n" + string(content), err
		}
	}
	port := openSocks(t)
	passed := 0
	for i := range 3 {
		d, b, err := Start(exe, t.TempDir(), Events{})
		if err != nil {
			t.Fatal(err)
		}
		ctx, err := d.Context("eth", port)
		if err != nil {
			t.Fatal(err)
		}
		tab, err := d.Open(ctx, turnstileDemo, true)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
		if err := d.Drive(tab); err != nil {
			t.Fatal(err)
		}
		d.Send(tab, map[string]any{"cmd": "record", "ifaceName": "Ethernet", "n": 1, "of": 1, "tokens": defaultTokens})
		time.Sleep(22 * time.Second)
		r, _ := d.call(d.session(tab), "Runtime.evaluate", map[string]any{"returnByValue": true,
			"expression": `JSON.stringify({token: (document.querySelector("[name=cf-turnstile-response]") || {value: ""}).value.length, webdriver: navigator.webdriver, bar: !!document.getElementById("dms-resolver-bar")})`})
		var v struct {
			Result struct{ Value string } `json:"result"`
		}
		json.Unmarshal(r, &v)
		t.Logf("driver run %d: %s", i+1, v.Result.Value)
		var got struct {
			Token     int
			Webdriver bool
		}
		json.Unmarshal([]byte(v.Result.Value), &got)
		if got.Webdriver {
			t.Error("navigator.webdriver is true: captcha services take the page for a bot")
		}
		if got.Token > 0 {
			passed++
		}
		b.Close()
	}
	if passed == 0 { // one run may lose to the service's own moods; three may not
		t.Error("the Turnstile never passed in the engine's browser")
	}
}

// openSocks is a SOCKS5 server for the test that connects anywhere (no auth),
// as an interface's proxy does for the engine's browser.
func openSocks(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
			go func() {
				defer c.Close()
				buf := make([]byte, 262)
				if _, err := io.ReadFull(c, buf[:2]); err != nil {
					return
				}
				io.ReadFull(c, buf[:buf[1]])
				c.Write([]byte{5, 0})
				if _, err := io.ReadFull(c, buf[:4]); err != nil {
					return
				}
				var host string
				switch buf[3] {
				case 1:
					io.ReadFull(c, buf[:4])
					host = net.IP(buf[:4]).String()
				case 3:
					io.ReadFull(c, buf[:1])
					n := int(buf[0])
					io.ReadFull(c, buf[:n])
					host = string(buf[:n])
				case 4:
					io.ReadFull(c, buf[:16])
					host = net.IP(buf[:16]).String()
				}
				io.ReadFull(c, buf[:2])
				addr := net.JoinHostPort(host, fmt.Sprint(binary.BigEndian.Uint16(buf[:2])))
				up, err := net.DialTimeout("tcp", addr, 10*time.Second)
				if err != nil {
					c.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer up.Close()
				c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				go io.Copy(up, c)
				io.Copy(c, up)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}
