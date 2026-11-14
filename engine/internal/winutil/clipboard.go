package winutil

import (
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	pOpenClipboard    = user32.NewProc("OpenClipboard")
	pCloseClipboard   = user32.NewProc("CloseClipboard")
	pGetClipboardData = user32.NewProc("GetClipboardData")
	pGlobalLock       = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalLock")
	pGlobalSize       = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalSize")
	pGlobalUnlock     = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalUnlock")
	pSetExecState     = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")
)

const cfUnicodeText = 13

// ClipboardText is the clipboard's text, "" if it holds none.
func ClipboardText() string {
	if r, _, _ := pOpenClipboard.Call(0); r == 0 {
		return ""
	}
	defer pCloseClipboard.Call()
	h, _, _ := pGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return ""
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		return ""
	}
	defer pGlobalUnlock.Call(h)
	size, _, _ := pGlobalSize.Call(h) // another process wrote the block: don't trust its NUL
	return utf16Text(unsafe.Slice((*uint16)(*(*unsafe.Pointer)(unsafe.Pointer(&p))), size/2))
}

// utf16Text is the text in units up to the first NUL, or all of them.
func utf16Text(units []uint16) string {
	if i := slices.Index(units, 0); i >= 0 {
		units = units[:i]
	}
	return windows.UTF16ToString(units)
}

// Links are the http, https and magnet links in text, one per word.
func Links(text string) []string {
	var out []string
	for _, w := range strings.Fields(text) {
		l := strings.ToLower(w)
		if strings.HasPrefix(l, "magnet:?") || strings.HasPrefix(l, "http://") && len(l) > 7 || strings.HasPrefix(l, "https://") && len(l) > 8 {
			out = append(out, w)
		}
	}
	return out
}

const esSystemRequired = 0x1

// StayAwake keeps Windows from going to sleep for the next while. Without
// ES_CONTINUOUS it only resets the idle timer, so it is called again every
// second while a download runs and simply not called when idle (the call
// needs no thread of its own).
func StayAwake() { pSetExecState.Call(esSystemRequired) }
