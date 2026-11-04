package window

import (
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                 = windows.NewLazySystemDLL("user32.dll")
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	pRegisterClassEx       = user32.NewProc("RegisterClassExW")
	pCreateWindowEx        = user32.NewProc("CreateWindowExW")
	pDestroyWindow         = user32.NewProc("DestroyWindow")
	pDefWindowProc         = user32.NewProc("DefWindowProcW")
	pShowWindow            = user32.NewProc("ShowWindow")
	pUpdateWindow          = user32.NewProc("UpdateWindow")
	pIsIconic              = user32.NewProc("IsIconic")
	pSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	pPostMessage           = user32.NewProc("PostMessageW")
	pSendMessage           = user32.NewProc("SendMessageW")
	pPostThreadMessage     = user32.NewProc("PostThreadMessageW")
	pPostQuitMessage       = user32.NewProc("PostQuitMessage")
	pGetMessage            = user32.NewProc("GetMessageW")
	pTranslateMessage      = user32.NewProc("TranslateMessage")
	pDispatchMessage       = user32.NewProc("DispatchMessageW")
	pGetCursorPos          = user32.NewProc("GetCursorPos")
	pMonitorFromPoint      = user32.NewProc("MonitorFromPoint")
	pGetMonitorInfo        = user32.NewProc("GetMonitorInfoW")
	pLoadCursor            = user32.NewProc("LoadCursorW")
	pSetThreadDpiAwareness = user32.NewProc("SetThreadDpiAwarenessContext")
	pGetDpiForSystem       = user32.NewProc("GetDpiForSystem")
	pGetCurrentThreadID    = kernel32.NewProc("GetCurrentThreadId")
)

const (
	className          = "DMSDownloadsWindow"
	wsOverlappedWindow = 0x00CF0000
	swShow             = 5
	swRestore          = 9

	wmDestroy       = 0x0002
	wmMove          = 0x0003
	wmSize          = 0x0005
	wmActivate      = 0x0006
	wmClose         = 0x0010
	wmGetMinMaxInfo = 0x0024
	wmSetIcon       = 0x0080
	wmMoving        = 0x0216
	wmApp           = 0x8000
)

type point struct{ x, y int32 }

type rect struct{ left, top, right, bottom int32 }

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   windows.Handle
	icon       windows.Handle
	cursor     windows.Handle
	background windows.Handle
	menuName   *uint16
	className  *uint16
	iconSm     windows.Handle
}

type minMaxInfo struct {
	reserved, maxSize, maxPosition, minTrackSize, maxTrackSize point
}

type monitorInfo struct {
	size    uint32
	monitor rect
	work    rect
	flags   uint32
}

var (
	classOnce sync.Once
	classErr  error
	minSize   atomic.Pointer[[2]int32] // the smallest the window may be dragged to, in pixels
)

func registerClass() error {
	classOnce.Do(func() {
		var inst windows.Handle
		windows.GetModuleHandleEx(0, nil, &inst)
		name, _ := windows.UTF16PtrFromString(className)
		arrow, _, _ := pLoadCursor.Call(0, 32512) // IDC_ARROW
		wc := wndClassEx{
			wndProc:   windows.NewCallback(wndProc),
			instance:  inst,
			cursor:    windows.Handle(arrow),
			className: name,
		}
		wc.size = uint32(unsafe.Sizeof(wc))
		if r, _, err := pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
			classErr = err
		}
	})
	return classErr
}

// wndProc takes lParam as a pointer, as WM_GETMINMAXINFO's is.
func wndProc(hwnd, m, wp uintptr, lp unsafe.Pointer) uintptr {
	vv, ok := views.Load(hwnd)
	if !ok {
		r, _, _ := pDefWindowProc.Call(hwnd, m, wp, uintptr(lp))
		return r
	}
	v := vv.(*view)
	switch m {
	case wmSize:
		v.chromium.Resize()
	case wmMove, wmMoving:
		v.chromium.NotifyParentWindowPositionChanged()
	case wmActivate:
		if wp&0xFFFF != 0 { // not WA_INACTIVE: the page gets the keyboard
			v.chromium.Focus()
		}
	case wmClose:
		pDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0) // ends this thread's loop, run's
		return 0
	case wmGetMinMaxInfo:
		if s := minSize.Load(); s != nil {
			mm := (*minMaxInfo)(lp)
			mm.minTrackSize = point{s[0], s[1]}
		}
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, m, wp, uintptr(lp))
	return r
}

// centred places a cx×cy window in the middle of the screen the mouse is on
// (where the tray icon was clicked), within its work area.
func centred(cx, cy int) (x, y, w, h int) {
	var p point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	mon, _, _ := pMonitorFromPoint.Call(uintptr(*(*uint64)(unsafe.Pointer(&p))), 1) // MONITOR_DEFAULTTOPRIMARY
	mi := monitorInfo{}
	mi.size = uint32(unsafe.Sizeof(mi))
	if r, _, _ := pGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return 100, 100, cx, cy
	}
	wa := mi.work
	aw, ah := int(wa.right-wa.left), int(wa.bottom-wa.top)
	w, h = min(cx, aw), min(cy, ah)
	return int(wa.left) + (aw-w)/2, int(wa.top) + (ah-h)/2, w, h
}
