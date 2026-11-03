package winutil

import (
	"errors"
	"log"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Balloons are shown on the tray icon fyne.io/systray creates: its window
// (class "SystrayClass" in this process) and its icon ID. The library doesn't
// expose balloons, so they are sent to its icon directly, and its window is
// subclassed to hear about clicks on them.
const (
	trayClass  = "SystrayClass"
	trayIconID = 100
	trayMsg    = 0x0400 + 1 // WM_USER+1, the library's callback message

	nimModify           = 0x1
	nifTip              = 0x4
	nifInfo             = 0x10
	niifUser            = 0x4
	niifLargeIcon       = 0x20
	ninBalloonUserClick = 0x0400 + 5
	gwlpWndProc         = ^uintptr(3) // -4
)

// AppName is what the tray tooltip starts with. Windows names a balloon (and
// its entry in the notification centre) after the tooltip.
const AppName = "DMS²"

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	shell32              = windows.NewLazySystemDLL("shell32.dll")
	pFindWindowEx        = user32.NewProc("FindWindowExW")
	pSetWindowLongPtr    = user32.NewProc("SetWindowLongPtrW")
	pCallWindowProc      = user32.NewProc("CallWindowProcW")
	pCreateIconFromResEx = user32.NewProc("CreateIconFromResourceEx")
	pShellNotifyIcon     = shell32.NewProc("Shell_NotifyIconW")
)

type notifyIconData struct {
	Size            uint32
	Wnd             windows.Handle
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            windows.Handle
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	TimeoutVersion  uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUIDItem        windows.GUID
	BalloonIcon     windows.Handle
}

var balloon struct {
	sync.Mutex
	wnd     windows.Handle
	oldProc uintptr
	icon    windows.Handle
	click   func() // the latest balloon's action
}

// SetBalloonIcon gives balloons the app's icon (ICO bytes holding one PNG
// image, as the tray uses).
func SetBalloonIcon(ico []byte) {
	if len(ico) < 22 {
		return
	}
	off := int(uint32(ico[18]) | uint32(ico[19])<<8 | uint32(ico[20])<<16 | uint32(ico[21])<<24)
	if off >= len(ico) {
		return
	}
	img := ico[off:]
	h, _, _ := pCreateIconFromResEx.Call(uintptr(unsafe.Pointer(&img[0])), uintptr(len(img)), 1, 0x00030000, 32, 32, 0)
	balloon.Lock()
	balloon.icon = windows.Handle(h)
	balloon.Unlock()
}

// Notify shows a balloon on the tray icon.
func Notify(title, msg string) { NotifyClick(title, msg, nil) }

// NotifyClick shows a balloon on the tray icon that runs click (if not nil)
// when the user clicks it. Shown in the background: right after startup the
// tray icon may take a moment to appear.
func NotifyClick(title, msg string, click func()) {
	go func() {
		var err error
		for range 20 {
			if err = showBalloon(title, msg, click); err == nil {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		log.Printf("balloon %q: %v", title, err)
	}()
}

func showBalloon(title, msg string, click func()) error {
	balloon.Lock()
	defer balloon.Unlock()
	if balloon.wnd == 0 {
		w := ownWindow(trayClass)
		if w == 0 {
			return errors.New("no tray icon yet")
		}
		proc := windows.NewCallback(trayProc)
		old, _, err := pSetWindowLongPtr.Call(uintptr(w), gwlpWndProc, proc)
		if old == 0 {
			return err
		}
		balloon.wnd, balloon.oldProc = w, old
	}
	nid := notifyIconData{
		Wnd:         balloon.wnd,
		ID:          trayIconID,
		Flags:       nifInfo | nifTip,
		InfoFlags:   niifUser | niifLargeIcon,
		BalloonIcon: balloon.icon,
	}
	nid.Size = uint32(unsafe.Sizeof(nid))
	copyUTF16(nid.Tip[:], AppName)
	copyUTF16(nid.InfoTitle[:], title)
	copyUTF16(nid.Info[:], msg)
	if ok, _, err := pShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&nid))); ok == 0 {
		return err
	}
	balloon.click = click
	return nil
}

func trayProc(hwnd windows.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	if msg == trayMsg && lParam&0xffff == ninBalloonUserClick {
		balloon.Lock()
		click := balloon.click
		balloon.click = nil
		balloon.Unlock()
		if click != nil {
			go click()
		}
		return 0
	}
	r, _, _ := pCallWindowProc.Call(balloon.oldProc, uintptr(hwnd), uintptr(msg), wParam, lParam)
	return r
}

// ownWindow is this process's top-level window of the class, 0 if none.
func ownWindow(class string) windows.Handle {
	c, _ := windows.UTF16PtrFromString(class)
	pid := windows.GetCurrentProcessId()
	var w uintptr
	for {
		w, _, _ = pFindWindowEx.Call(0, w, uintptr(unsafe.Pointer(c)), 0)
		if w == 0 {
			return 0
		}
		var owner uint32
		windows.GetWindowThreadProcessId(windows.HWND(w), &owner)
		if owner == pid {
			return windows.Handle(w)
		}
	}
}

// copyUTF16 fills a fixed buffer, cut to fit at a character boundary and
// always NUL-terminated.
func copyUTF16(dst []uint16, s string) {
	u := windows.StringToUTF16(CutUTF16(s, len(dst)-1))
	copy(dst, u)
}

// CutUTF16 is the longest start of s that fits in max UTF-16 units, cut
// between characters. Windows counts tooltip and balloon lengths in those units.
func CutUTF16(s string, max int) string {
	n := 0
	for i, r := range s {
		w := 1
		if r > 0xffff {
			w = 2
		}
		if n+w > max {
			return s[:i]
		}
		n += w
	}
	return s
}
