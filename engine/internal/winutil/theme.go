package winutil

import (
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var pDwmSetWindowAttribute = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")

// AppsDark is whether Windows is set to dark mode for apps.
func AppsDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

// SetTitleBar makes a window's title bar dark or light (Windows 10 and 11)
// and, on Windows 11, gives it the caption and text colours (0xRRGGBB), so it
// matches what the window draws below it.
func SetTitleBar(hwnd uintptr, dark bool, caption, text uint32) {
	if hwnd == 0 || pDwmSetWindowAttribute.Find() != nil {
		return
	}
	on := uint32(0)
	if dark {
		on = 1
	}
	capRef, textRef := colorref(caption), colorref(text)
	for _, a := range []struct {
		attr uintptr
		v    *uint32
	}{{20, &on}, {35, &capRef}, {36, &textRef}} { // immersive dark mode, caption colour, caption text colour
		pDwmSetWindowAttribute.Call(hwnd, a.attr, uintptr(unsafe.Pointer(a.v)), 4)
	}
}

// colorref turns 0xRRGGBB into a Windows COLORREF (0x00BBGGRR).
func colorref(c uint32) uint32 { return c>>16&0xff | c&0xff00 | (c&0xff)<<16 }
