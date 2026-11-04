// Package window is the Downloads window: the extension's pages shown in
// WebView2 beside the tray, talking to the engine over an in-memory pipe the
// engine serves like the named pipe.
//
// The window's message loop runs on an OS thread of its own; systray keeps the
// main thread. Closing the window (✕) destroys the webview and frees
// WebView2's processes; the engine stays in the tray.
package window

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/url"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"

	"downloadmanagersquared/engine/internal/winutil"
)

const (
	title = "DMS² Downloads"
	// The pages are served from here by the window itself: .invalid never
	// resolves, so nothing reaches the network.
	origin = "https://dms.invalid/"
	// In 96-DPI pixels.
	width, height       = 900, 720
	minWidth, minHeight = 420, 360
)

// Settings is Open's view for the settings page, as the tray asks for it. Any
// other "#hash" opens the settings page there too, keeping the hash (e.g. "#pia").
const Settings = "#settings"

// Config is what the window needs from the engine.
type Config struct {
	DataDir string          // the engine's: WebView2 keeps its profile in DataDir\webview
	Dial    func() net.Conn // a client of the engine (Engine.Dial)
	Icon    []byte          // ICO bytes, the tray's
	Pages   fs.FS           // ui/popup.html, ui/settings.html, lib/version.js...; nil if missing
}

// Window is the Downloads window, open or not. Its methods are safe from any
// goroutine.
type Window struct {
	cfg Config

	mu      sync.Mutex
	open    *view // nil while closed
	opening bool
}

func New(cfg Config) *Window { return &Window{cfg: cfg} }

// Open shows view: "" is the downloads, Settings (or any other "#hash") the
// settings page. An open window comes to the front and switches to it. It
// fails only if WebView2 can't be created (its runtime is missing).
func (w *Window) Open(view string) error {
	u := pageURL(view)
	w.mu.Lock()
	if v := w.open; v != nil {
		w.mu.Unlock()
		v.dispatch(func() { v.show(u) })
		return nil
	}
	if w.opening {
		w.mu.Unlock()
		return nil
	}
	w.opening = true
	w.mu.Unlock()
	ready := make(chan error, 1)
	go w.run(u, ready)
	return <-ready
}

// Close closes an open window, as its ✕ does, and waits (at most 5 s) until
// its message loop has ended. For quitting: never end the loop from another
// thread, as that ends only the calling thread's loop.
func (w *Window) Close() {
	w.mu.Lock()
	v := w.open
	w.mu.Unlock()
	if v == nil {
		return
	}
	pPostMessage.Call(v.hwnd, wmClose, 0, 0)
	select {
	case <-v.done:
	case <-time.After(5 * time.Second):
		log.Printf("the Downloads window didn't close within 5 s")
	}
}

func pageURL(view string) string {
	switch {
	case view == "":
		return origin + "ui/popup.html?window" // ?window: the popup doesn't close itself
	case view == Settings:
		return origin + "ui/settings.html"
	}
	return origin + "ui/settings.html" + view
}

// run is the window's thread: it creates the window, then runs its message
// loop until the window is destroyed.
func (w *Window) run(u string, ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	v, err := w.create(u)
	w.mu.Lock()
	w.opening = false
	if err == nil {
		w.open = v
	}
	w.mu.Unlock()
	ready <- err
	if err != nil {
		return
	}
	v.loop()
	v.bridge.close()
	w.mu.Lock()
	w.open = nil
	w.mu.Unlock()
	views.Delete(v.hwnd)
	close(v.done)
}

// view is one open window, from creation until it is destroyed.
type view struct {
	hwnd     uintptr
	thread   uint32
	chromium *edge.Chromium
	wv       *comObject
	bridge   *bridge
	pages    fs.FS
	done     chan struct{}

	mu    sync.Mutex
	queue []func()
}

var views sync.Map // hwnd → *view, for wndProc

func (w *Window) create(u string) (*view, error) {
	windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
	if pSetThreadDpiAwareness.Find() == nil {
		pSetThreadDpiAwareness.Call(^uintptr(1)) // DPI_AWARENESS_CONTEXT_SYSTEM_AWARE (-2)
	}
	if err := registerClass(); err != nil {
		return nil, err
	}
	dpi := uintptr(96)
	if pGetDpiForSystem.Find() == nil {
		if d, _, _ := pGetDpiForSystem.Call(); d != 0 {
			dpi = d
		}
	}
	scale := func(n int) int { return n * int(dpi) / 96 }
	x, y, cx, cy := centred(scale(width), scale(height))

	v := &view{pages: w.cfg.Pages, done: make(chan struct{})}
	t, _, _ := pGetCurrentThreadID.Call()
	v.thread = uint32(t)
	cls, _ := windows.UTF16PtrFromString(className)
	name, _ := windows.UTF16PtrFromString(title)
	hwnd, _, err := pCreateWindowEx.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(name)),
		wsOverlappedWindow, uintptr(x), uintptr(y), uintptr(cx), uintptr(cy), 0, 0, 0, 0)
	if hwnd == 0 {
		return nil, err
	}
	v.hwnd = hwnd
	views.Store(hwnd, v)
	matchTitleBar(hwnd)
	minSize.Store(&[2]int32{int32(scale(minWidth)), int32(scale(minHeight))})
	if big := winutil.Icon(w.cfg.Icon, scale(32)); big != 0 {
		pSendMessage.Call(hwnd, wmSetIcon, 1, uintptr(big)) // ICON_BIG
	}
	if small := winutil.Icon(w.cfg.Icon, scale(16)); small != 0 {
		pSendMessage.Call(hwnd, wmSetIcon, 0, uintptr(small)) // ICON_SMALL
	}

	c := edge.NewChromium()
	c.DataPath = filepath.Join(w.cfg.DataDir, "webview")
	c.MessageCallback = v.fromPage
	c.WebResourceRequestedCallback = v.serve
	v.chromium = c
	pShowWindow.Call(hwnd, swShow)
	pUpdateWindow.Call(hwnd)
	if !c.Embed(hwnd) {
		views.Delete(hwnd)
		pDestroyWindow.Call(hwnd)
		return nil, errors.New("WebView2 could not start (is its runtime installed?)")
	}
	c.Resize()
	if s, err := c.GetSettings(); err == nil {
		s.PutAreDevToolsEnabled(false)
		s.PutIsStatusBarEnabled(false)
	}
	c.AddWebResourceRequestedFilter(origin+"*", edge.COREWEBVIEW2_WEB_RESOURCE_CONTEXT_ALL)
	if v.wv = coreWebView(c); v.wv != nil {
		onNewWindow(v.wv, openOutside)
		onNavigation(v.wv, ours, openOutside)
	}
	c.Init(bridgeScript)
	v.bridge = newBridge(w.cfg.Dial(), v.toPage)
	if v.pages != nil {
		c.Navigate(u)
	} else {
		c.NavigateToString(missingPage)
	}
	pSetForegroundWindow.Call(hwnd)
	c.Focus()
	return v, nil
}

// bridgeScript runs before each page's own: window.dmsWindow is how the
// window adapter in host.js reaches the engine and the default browser.
const bridgeScript = `(() => {
  const post = (m) => window.chrome.webview.postMessage(JSON.stringify(m));
  window.dmsWindow = {
    send: (line) => post({ engine: line }),
    openUrl: (url) => post({ openUrl: url }),
    onLine: null,
    receive(line) { if (this.onLine) this.onLine(line); },
  };
})();`

const missingPage = `<!doctype html><meta charset="utf-8"><title>DMS² Downloads</title>
<body style="font:15px system-ui,sans-serif;padding:32px">Pages missing: build with build.ps1</body>`

// fromPage is a message from the page: a request for the engine, or a link
// to open in the default browser.
func (v *view) fromPage(msg string) {
	var m struct {
		Engine  string `json:"engine"`
		OpenURL string `json:"openUrl"`
	}
	if json.Unmarshal([]byte(msg), &m) != nil {
		return
	}
	if m.Engine != "" {
		v.bridge.send(m.Engine)
	}
	if m.OpenURL != "" {
		openOutside(m.OpenURL)
	}
}

// toPage hands the page a line from the engine, on the window's thread.
func (v *view) toPage(line string) {
	js, _ := json.Marshal(line)
	v.dispatch(func() { v.chromium.Eval("window.dmsWindow && window.dmsWindow.receive(" + string(js) + ")") })
}

// show brings the window to the front on u, unless it already shows it.
func (v *view) show(u string) {
	if v.wv == nil || getString(v.wv, webviewGetSource) != u {
		v.chromium.Navigate(u)
	}
	if r, _, _ := pIsIconic.Call(v.hwnd); r != 0 {
		pShowWindow.Call(v.hwnd, swRestore)
	}
	pSetForegroundWindow.Call(v.hwnd)
	v.chromium.Focus()
}

const webviewGetSource = 4 // ICoreWebView2::get_Source

// serve answers the pages' requests to origin from the carried pages.
func (v *view) serve(req *edge.ICoreWebView2WebResourceRequest, args *edge.ICoreWebView2WebResourceRequestedEventArgs) {
	uri, err := req.GetUri()
	if err != nil || !strings.HasPrefix(uri, origin) {
		return
	}
	name := strings.TrimPrefix(uri, origin)
	if i := strings.IndexAny(name, "?#"); i >= 0 {
		name = name[:i]
	}
	env := v.chromium.Environment()
	var body []byte
	if v.pages != nil && fs.ValidPath(name) {
		body, err = fs.ReadFile(v.pages, name)
	}
	var resp *edge.ICoreWebView2WebResourceResponse
	if body == nil || err != nil {
		resp, err = env.CreateWebResourceResponse(nil, 404, "Not Found", "")
	} else {
		resp, err = env.CreateWebResourceResponse(body, 200, "OK", "Content-Type: "+contentType(name)+"\r\nCache-Control: no-store")
	}
	if err == nil {
		args.PutResponse(resp)
	}
}

func contentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	}
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// ours is where the window may navigate: its pages, and the "pages missing" note.
func ours(uri string) bool {
	return strings.HasPrefix(uri, origin) || strings.HasPrefix(uri, "data:") || uri == "about:blank"
}

// openOutside opens a web link in the default browser; nothing else is opened.
func openOutside(uri string) {
	u, err := url.Parse(uri)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || ours(uri) {
		return
	}
	go func() {
		if err := winutil.ShellOpen(u.String()); err != nil {
			log.Printf("opening %s: %v", u.Redacted(), err)
		}
	}()
}

// dispatch runs f on the window's thread.
func (v *view) dispatch(f func()) {
	v.mu.Lock()
	v.queue = append(v.queue, f)
	v.mu.Unlock()
	pPostThreadMessage.Call(uintptr(v.thread), wmApp, 0, 0)
}

func (v *view) loop() {
	var m msg
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // WM_QUIT, or an error
			return
		}
		if m.hwnd == 0 && m.message == wmApp {
			v.mu.Lock()
			q := v.queue
			v.queue = nil
			v.mu.Unlock()
			for _, f := range q {
				f()
			}
			continue
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}
