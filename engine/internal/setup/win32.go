package setup

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Win32 the window needs, called directly.

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")

	pRegisterClassEx     = user32.NewProc("RegisterClassExW")
	pCreateWindowEx      = user32.NewProc("CreateWindowExW")
	pDefWindowProc       = user32.NewProc("DefWindowProcW")
	pDestroyWindow       = user32.NewProc("DestroyWindow")
	pGetMessage          = user32.NewProc("GetMessageW")
	pIsDialogMessage     = user32.NewProc("IsDialogMessageW")
	pTranslateMessage    = user32.NewProc("TranslateMessage")
	pDispatchMessage     = user32.NewProc("DispatchMessageW")
	pPostQuitMessage     = user32.NewProc("PostQuitMessage")
	pPostMessage         = user32.NewProc("PostMessageW")
	pSendMessage         = user32.NewProc("SendMessageW")
	pGetWindowText       = user32.NewProc("GetWindowTextW")
	pGetWindowTextLength = user32.NewProc("GetWindowTextLengthW")
	pSetWindowText       = user32.NewProc("SetWindowTextW")
	pShowWindow          = user32.NewProc("ShowWindow")
	pSetForeground       = user32.NewProc("SetForegroundWindow")
	pSetFocus            = user32.NewProc("SetFocus")
	pEnableWindow        = user32.NewProc("EnableWindow")
	pIsWindowEnabled     = user32.NewProc("IsWindowEnabled")
	pAdjustWindowRectEx  = user32.NewProc("AdjustWindowRectEx")
	pGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	pLoadCursor          = user32.NewProc("LoadCursorW")
	pCreateIconFromRes   = user32.NewProc("CreateIconFromResourceEx")
	pDrawIconEx          = user32.NewProc("DrawIconEx")
	pSetDpiAwareness     = user32.NewProc("SetProcessDpiAwarenessContext")
	pGetDpiForSystem     = user32.NewProc("GetDpiForSystem")
	pBeginPaint          = user32.NewProc("BeginPaint")
	pEndPaint            = user32.NewProc("EndPaint")
	pInvalidateRect      = user32.NewProc("InvalidateRect")
	pGetClientRect       = user32.NewProc("GetClientRect")
	pFillRect            = user32.NewProc("FillRect")
	pDrawText            = user32.NewProc("DrawTextW")
	pSetTimer            = user32.NewProc("SetTimer")
	pKillTimer           = user32.NewProc("KillTimer")
	pGetCursorPos        = user32.NewProc("GetCursorPos")
	pWindowFromPoint     = user32.NewProc("WindowFromPoint")

	pCreateFont       = gdi32.NewProc("CreateFontW")
	pSetTextColor     = gdi32.NewProc("SetTextColor")
	pSetBkColor       = gdi32.NewProc("SetBkColor")
	pSetBkMode        = gdi32.NewProc("SetBkMode")
	pSelectObject     = gdi32.NewProc("SelectObject")
	pDeleteObject     = gdi32.NewProc("DeleteObject")
	pCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	pCreateCompatDC   = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatBmp  = gdi32.NewProc("CreateCompatibleBitmap")
	pBitBlt           = gdi32.NewProc("BitBlt")
	pDeleteDC         = gdi32.NewProc("DeleteDC")

	pGetModuleHandle = kernel32.NewProc("GetModuleHandleW")
	pCreateActCtx    = kernel32.NewProc("CreateActCtxW")
	pActivateActCtx  = kernel32.NewProc("ActivateActCtx")

	pDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

const (
	wmDestroy        = 0x0002
	wmPaint          = 0x000F
	wmClose          = 0x0010
	wmEraseBkgnd     = 0x0014
	wmSettingChange  = 0x001A
	wmSetCursor      = 0x0020
	wmDrawItem       = 0x002B
	wmSetFont        = 0x0030
	wmSetIcon        = 0x0080
	wmCommand        = 0x0111
	wmTimer          = 0x0113
	wmCtlColorEdit   = 0x0133
	wmCtlColorStatic = 0x0138
	wmInstalled      = 0x8000 + 1 // WM_APP+1: the install finished
	emSetSel         = 0x00B1
	emSetMargins     = 0x00D3

	wsChild           = 0x40000000
	wsVisible         = 0x10000000
	wsTabStop         = 0x00010000
	wsGroup           = 0x00020000
	wsCaption         = 0x00C00000
	wsSysMenu         = 0x00080000
	wsMinimizeBox     = 0x00020000
	wsExControlParent = 0x00010000
	esAutoHScroll     = 0x0080
	esReadOnly        = 0x0800
	bsOwnerDraw       = 0x000B
	enSetFocus        = 0x0100
	enKillFocus       = 0x0200

	odsSelected    = 0x0001
	odsDisabled    = 0x0004
	odsFocus       = 0x0010
	odsNoFocusRect = 0x0200

	dtCenter      = 0x0001
	dtVCenter     = 0x0004
	dtWordBreak   = 0x0010
	dtSingleLine  = 0x0020
	dtNoPrefix    = 0x0800
	dtEndEllipsis = 0x8000
)

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

type msg struct {
	hwnd    windows.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      [2]int32
	private uint32
}

type rect struct{ left, top, right, bottom int32 }

type paintStruct struct {
	hdc       windows.Handle
	erase     int32
	paint     rect
	restore   int32
	incUpdate int32
	reserved  [32]byte
}

type drawItemStruct struct {
	ctlType, ctlID, itemID, itemAction, itemState uint32
	hwndItem                                      windows.Handle
	hdc                                           windows.Handle
	item                                          rect
	itemData                                      uintptr
}

func str(s string) uintptr {
	p, _ := windows.UTF16PtrFromString(s)
	return uintptr(unsafe.Pointer(p))
}

func text(h windows.Handle) string {
	n, _, _ := pGetWindowTextLength.Call(uintptr(h))
	buf := make([]uint16, n+1)
	pGetWindowText.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), n+1)
	return windows.UTF16ToString(buf)
}

func invalidate(h windows.Handle) { pInvalidateRect.Call(uintptr(h), 0, 0) }

func enabled(h windows.Handle) bool {
	r, _, _ := pIsWindowEnabled.Call(uintptr(h))
	return r != 0
}

// iconHandle makes an icon of ICO bytes holding one PNG image.
func iconHandle(ico []byte, size int32) windows.Handle {
	if len(ico) < 22 {
		return 0
	}
	off := int(uint32(ico[18]) | uint32(ico[19])<<8 | uint32(ico[20])<<16 | uint32(ico[21])<<24)
	if off >= len(ico) {
		return 0
	}
	img := ico[off:]
	h, _, _ := pCreateIconFromRes.Call(uintptr(unsafe.Pointer(&img[0])), uintptr(len(img)), 1, 0x00030000, uintptr(size), uintptr(size), 0)
	return windows.Handle(h)
}

// modernControls makes this thread's edit fields version 6 of the common
// controls rather than the Windows 95 ones an exe without a manifest gets: it
// activates a manifest asking for them, then loads the library under it.
func modernControls() {
	const manifest = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <dependency><dependentAssembly>
    <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0" processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/>
  </dependentAssembly></dependency>
</assembly>`
	f, err := os.CreateTemp("", "dms-setup-*.manifest")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	f.WriteString(manifest)
	f.Close()
	type actCtx struct {
		size         uint32
		flags        uint32
		source       *uint16
		arch, langID uint16
		assemblyDir  *uint16
		resourceName *uint16
		appName      *uint16
		module       windows.Handle
	}
	ctx := actCtx{source: windows.StringToUTF16Ptr(f.Name())}
	ctx.size = uint32(unsafe.Sizeof(ctx))
	h, _, _ := pCreateActCtx.Call(uintptr(unsafe.Pointer(&ctx)))
	if windows.Handle(h) == windows.InvalidHandle {
		return
	}
	var cookie uintptr
	pActivateActCtx.Call(h, uintptr(unsafe.Pointer(&cookie)))
	// By name, so the activated manifest redirects it to version 6.
	if lib, err := syscall.LoadLibrary("comctl32.dll"); err == nil {
		if p, err := syscall.GetProcAddress(lib, "InitCommonControls"); err == nil {
			syscall.SyscallN(p)
		}
	}
}
