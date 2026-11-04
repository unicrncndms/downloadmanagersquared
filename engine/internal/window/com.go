package window

import (
	"sync"
	"syscall"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

// go-webview2 wraps only some of WebView2's events. The two the window needs
// besides (new windows, and navigations away from the pages) are registered
// here by their slot in WebView2's COM interfaces (WebView2.h).

// comObject is any COM object: a pointer to its table of methods.
type comObject struct{ vtbl *[64]uintptr }

func (o *comObject) call(slot int, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(o.vtbl[slot], append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...)
	return r
}

// Slots in ICoreWebView2Controller, ICoreWebView2 and the events' arguments.
const (
	controllerGetCoreWebView2 = 25
	webviewAddNavigationStart = 7
	webviewAddNewWindow       = 44

	navStartGetURI    = 3
	navStartPutCancel = 8
	newWindowGetURI   = 3
	newWindowPutDone  = 6 // put_Handled
)

// eventHandler is a COM event handler (QueryInterface, AddRef, Release, Invoke)
// that calls fn. Every handler shares one method table: Windows allows a
// process only so many callbacks.
type eventHandler struct {
	vtbl *[4]uintptr
	fn   func(args *comObject)
}

var (
	handlerVtbl     [4]uintptr
	handlerVtblOnce sync.Once
	handlersMu      sync.Mutex
	handlers        []*eventHandler // kept from the garbage collector while WebView2 holds them
)

func newEventHandler(fn func(args *comObject)) *eventHandler {
	handlerVtblOnce.Do(func() {
		handlerVtbl = [4]uintptr{
			windows.NewCallback(func(h *eventHandler, _ uintptr, out *unsafe.Pointer) uintptr {
				*out = unsafe.Pointer(h)
				return 0
			}),
			windows.NewCallback(func(*eventHandler) uintptr { return 1 }),
			windows.NewCallback(func(*eventHandler) uintptr { return 1 }),
			windows.NewCallback(func(h *eventHandler, _ *comObject, args *comObject) uintptr {
				h.fn(args)
				return 0
			}),
		}
	})
	h := &eventHandler{vtbl: &handlerVtbl, fn: fn}
	handlersMu.Lock()
	handlers = append(handlers, h)
	handlersMu.Unlock()
	return h
}

// getString calls a getter that hands back a string WebView2 allocated.
func getString(o *comObject, slot int) string {
	var p *uint16
	o.call(slot, uintptr(unsafe.Pointer(&p)))
	if p == nil {
		return ""
	}
	s := windows.UTF16PtrToString(p)
	windows.CoTaskMemFree(unsafe.Pointer(p))
	return s
}

// coreWebView is the ICoreWebView2 of an embedded Chromium.
func coreWebView(c *edge.Chromium) *comObject {
	ctl := (*comObject)(unsafe.Pointer(c.GetController()))
	if ctl == nil {
		return nil
	}
	var wv *comObject
	ctl.call(controllerGetCoreWebView2, uintptr(unsafe.Pointer(&wv)))
	return wv
}

// onNewWindow handles every request for a new window (target="_blank",
// window.open): none opens, and the page's URL goes to open.
func onNewWindow(wv *comObject, open func(uri string)) {
	h := newEventHandler(func(args *comObject) {
		uri := getString(args, newWindowGetURI)
		args.call(newWindowPutDone, 1)
		open(uri)
	})
	var token int64
	wv.call(webviewAddNewWindow, uintptr(unsafe.Pointer(h)), uintptr(unsafe.Pointer(&token)))
}

// onNavigation lets the page navigate only where allow says, and hands any
// other URL to elsewhere.
func onNavigation(wv *comObject, allow func(uri string) bool, elsewhere func(uri string)) {
	h := newEventHandler(func(args *comObject) {
		uri := getString(args, navStartGetURI)
		if allow(uri) {
			return
		}
		args.call(navStartPutCancel, 1)
		elsewhere(uri)
	})
	var token int64
	wv.call(webviewAddNavigationStart, uintptr(unsafe.Pointer(h)), uintptr(unsafe.Pointer(&token)))
}
