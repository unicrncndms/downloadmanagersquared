package winutil

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Bringing a dialog to the front from the engine, which runs in the
// background: Windows keeps a background process's new windows behind the
// one in front (Firefox, when the folder picker is asked for from the
// toolbar popup). The dialog is given an owner window that stays on top and
// takes the foreground, with this thread briefly sharing the foreground
// window's input, which Windows allows.

var (
	pCreateWindowExW          = user32.NewProc("CreateWindowExW")
	pDestroyWindow            = user32.NewProc("DestroyWindow")
	pShowWindow               = user32.NewProc("ShowWindow")
	pGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	pGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	pAttachThreadInput        = user32.NewProc("AttachThreadInput")
	pSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	pBringWindowToTop         = user32.NewProc("BringWindowToTop")
	pGetCursorPos             = user32.NewProc("GetCursorPos")
	pGetWindowRect            = user32.NewProc("GetWindowRect")
	pIsWindowVisible          = user32.NewProc("IsWindowVisible")
	pGetCurrentThreadId       = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCurrentThreadId")
)

const (
	wsPopup         = 0x80000000
	wsExTopmost     = 0x00000008
	wsExToolWindow  = 0x00000080
	swShowNoActive  = 4
	frontOwnerClass = "STATIC" // a predefined class: nothing to register
)

// frontOwner makes a tiny, always-on-top window that has the foreground, to
// own a dialog shown from the background, placed over near when near is a
// visible window, else at the mouse pointer (where the click that asked for
// it was). The dialog centres itself on it. It must be called on the thread
// that shows the dialog; the returned func destroys it.
func frontOwner(near windows.Handle) (windows.Handle, func()) {
	if near != 0 && foregroundIsOurs() {
		return near, func() {} // already in front: near owns it, and is disabled while it shows
	}
	x, y := pointerOr(near)
	cls, _ := windows.UTF16PtrFromString(frontOwnerClass)
	h, _, _ := pCreateWindowExW.Call(wsExTopmost|wsExToolWindow, uintptr(unsafe.Pointer(cls)), 0, wsPopup,
		uintptr(x), uintptr(y), 0, 0, uintptr(near), 0, 0, 0)
	if h == 0 {
		return near, func() {}
	}
	pShowWindow.Call(h, swShowNoActive)
	takeForeground(h)
	return windows.Handle(h), func() { pDestroyWindow.Call(h) }
}

// takeForeground brings h to the front, sharing the foreground window's input
// for the moment it takes.
func takeForeground(h uintptr) {
	fg, _, _ := pGetForegroundWindow.Call()
	cur, _, _ := pGetCurrentThreadId.Call()
	var other uintptr
	if fg != 0 {
		other, _, _ = pGetWindowThreadProcessId.Call(fg, 0)
	}
	if other != 0 && other != cur {
		if r, _, _ := pAttachThreadInput.Call(cur, other, 1); r != 0 {
			defer pAttachThreadInput.Call(cur, other, 0)
		}
	}
	pBringWindowToTop.Call(h)
	pSetForegroundWindow.Call(h)
}

// foregroundIsOurs: the window in front belongs to this process.
func foregroundIsOurs() bool {
	fg, _, _ := pGetForegroundWindow.Call()
	if fg == 0 {
		return false
	}
	var pid uint32
	pGetWindowThreadProcessId.Call(fg, uintptr(unsafe.Pointer(&pid)))
	return pid == windows.GetCurrentProcessId()
}

// pointerOr is the centre of near when it is a visible window, else where the
// mouse pointer is.
func pointerOr(near windows.Handle) (int32, int32) {
	if near != 0 {
		if v, _, _ := pIsWindowVisible.Call(uintptr(near)); v != 0 {
			var r struct{ Left, Top, Right, Bottom int32 }
			if ok, _, _ := pGetWindowRect.Call(uintptr(near), uintptr(unsafe.Pointer(&r))); ok != 0 {
				return (r.Left + r.Right) / 2, (r.Top + r.Bottom) / 2
			}
		}
	}
	var p struct{ X, Y int32 }
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return p.X, p.Y
}
