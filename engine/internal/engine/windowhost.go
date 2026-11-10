package engine

import (
	"log"
)

// Window is the Downloads window (internal/window), or a fake in tests.
type Window interface {
	// Open shows view: "" the downloads, "#settings" (or another "#hash") the
	// settings page. It fails only if the window can't be created.
	Open(view string) error
}

// windowHandle is a Downloads window that can say which window it is, so a
// dialog opens over it: the real one (Handle is 0 while it is closed).
type windowHandle interface{ Handle() uintptr }

// SetWindow gives the engine the Downloads window, for OpenManager.
func (e *Engine) SetWindow(w Window) {
	e.mu.Lock()
	e.window = w
	e.mu.Unlock()
}

// OpenManager shows view ("" the downloads, "#settings" or another "#hash" the
// settings page) in the Downloads window. Only if the window can't be created
// (WebView2 missing) does it fall back to the browser the extension last ran in.
func (e *Engine) OpenManager(view string) {
	e.mu.Lock()
	w := e.window
	e.mu.Unlock()
	if w == nil {
		e.openInBrowser(view)
		return
	}
	err := w.Open(view)
	if err == nil {
		return
	}
	e.mu.Lock()
	first := !e.windowFailed
	e.windowFailed = true
	e.mu.Unlock()
	if first {
		log.Printf("the Downloads window: %v; showing it in the browser instead", err)
	}
	e.openInBrowser(view)
}

// openInBrowser is OpenManager's fallback, the only path left that asks a
// browser to show the UI: a connected extension is told to show view;
// otherwise the browser the extension last ran in is started (its toolbar
// button then shows the downloads).
func (e *Engine) openInBrowser(view string) {
	e.mu.Lock()
	c := e.extensionLocked()
	browser := e.browser
	e.mu.Unlock()
	if c != nil {
		c.Emit("openManager", map[string]string{"hash": view})
		return
	}
	if err := startBrowser(browser, ""); err != nil {
		log.Printf("starting the browser: %v", err)
	}
}
