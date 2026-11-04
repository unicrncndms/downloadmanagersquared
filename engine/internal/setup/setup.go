// Package setup is the window a first install shows: where to install and
// what to set up besides the engine, then, once installed, the one step left
// (adding the browser extension). It is plain Win32, drawn with the system's
// current controls.
package setup

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"downloadmanagersquared/engine/internal/winutil"
)

// Choice is what the user set in the window.
type Choice struct {
	Dir                           string
	StartMenu, Desktop, Autostart bool
}

// Steps are what the window does for the user.
type Steps struct {
	Install      func(Choice) error // runs off the window's thread
	AddExtension func()             // the finished page's button
	ExtensionURL string             // shown to copy into another browser
}

// ErrReported is an Install failure the user was already told about.
var ErrReported = errors.New("already reported")

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pRegisterClassEx     = user32.NewProc("RegisterClassExW")
	pCreateWindowEx      = user32.NewProc("CreateWindowExW")
	pDefWindowProc       = user32.NewProc("DefWindowProcW")
	pDestroyWindow       = user32.NewProc("DestroyWindow")
	pGetMessage          = user32.NewProc("GetMessageW")
	pIsDialogMessage     = user32.NewProc("IsDialogMessageW")
	pTranslateMessage    = user32.NewProc("TranslateMessage")
	pDispatchMessage     = user32.NewProc("DispatchMessageW")
	pPostQuitMessage     = user32.NewProc("PostQuitMessage")
	pSendMessage         = user32.NewProc("SendMessageW")
	pGetWindowText       = user32.NewProc("GetWindowTextW")
	pGetWindowTextLength = user32.NewProc("GetWindowTextLengthW")
	pSetWindowText       = user32.NewProc("SetWindowTextW")
	pShowWindow          = user32.NewProc("ShowWindow")
	pSetForeground       = user32.NewProc("SetForegroundWindow")
	pSetFocus            = user32.NewProc("SetFocus")
	pPostMessage         = user32.NewProc("PostMessageW")
	pEnableWindow        = user32.NewProc("EnableWindow")
	pAdjustWindowRectEx  = user32.NewProc("AdjustWindowRectEx")
	pGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	pLoadCursor          = user32.NewProc("LoadCursorW")
	pGetSysColorBrush    = user32.NewProc("GetSysColorBrush")
	pGetSysColor         = user32.NewProc("GetSysColor")
	pCreateIconFromRes   = user32.NewProc("CreateIconFromResourceEx")
	pSetDpiAwareness     = user32.NewProc("SetProcessDpiAwarenessContext")
	pGetDpiForSystem     = user32.NewProc("GetDpiForSystem")
	pCreateFont          = gdi32.NewProc("CreateFontW")
	pSetTextColor        = gdi32.NewProc("SetTextColor")
	pSetBkMode           = gdi32.NewProc("SetBkMode")
	pGetModuleHandle     = kernel32.NewProc("GetModuleHandleW")
	pCreateActCtx        = kernel32.NewProc("CreateActCtxW")
	pActivateActCtx      = kernel32.NewProc("ActivateActCtx")
)

const (
	wmDestroy         = 0x0002
	wmClose           = 0x0010
	wmSetFont         = 0x0030
	wmSetIcon         = 0x0080
	wmCommand         = 0x0111
	wmCtlColorStatic  = 0x0138
	wmInstalled       = 0x8000 + 1 // WM_APP+1: the install finished
	esReadOnly        = 0x0800
	bmGetCheck        = 0x00F0
	bmSetCheck        = 0x00F1
	emSetSel          = 0x00B1
	wsChild           = 0x40000000
	wsVisible         = 0x10000000
	wsTabStop         = 0x00010000
	wsGroup           = 0x00020000
	wsCaption         = 0x00C00000
	wsSysMenu         = 0x00080000
	wsMinimizeBox     = 0x00020000
	wsExClientEdge    = 0x00000200
	wsExControlParent = 0x00010000
	esAutoHScroll     = 0x0080
	bsDefPushButton   = 0x0001
	bsAutoCheckbox    = 0x0003
	colorBtnFace      = 15
	colorGrayText     = 17
	idOK              = 1
	idCancel          = 2
	idBrowse          = 100
	idDir             = 101
	idStartMenu       = 102
	idDesktop         = 103
	idAutostart       = 104
	idNote            = 105
	idAddExtension    = 106
	idURL             = 107
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

// The window's state; there is only ever one.
var w struct {
	main, dir     windows.Handle
	install       windows.Handle   // the Install button
	page          []windows.Handle // the controls of the page showing
	grays         []windows.Handle // statics in gray
	boxes         map[int]windows.Handle
	steps         Steps
	installing    bool
	installErr    chan error
	installed     bool
	folderName    string
	dpi           int32
	font, heading windows.Handle
}

func str(s string) uintptr {
	p, _ := windows.UTF16PtrFromString(s)
	return uintptr(unsafe.Pointer(p))
}

func px(v int32) int32 { return v * w.dpi / 96 }

// Run shows the window until the user closes it, and says whether it
// installed. defaults fills it in; the last part of defaults.Dir is the folder
// Browse… adds to what the user picks. icon is ICO bytes holding one PNG image.
func Run(defaults Choice, icon []byte, steps Steps) bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	modernControls()
	if pSetDpiAwareness.Find() == nil {
		pSetDpiAwareness.Call(^uintptr(1)) // DPI_AWARENESS_CONTEXT_SYSTEM_AWARE (-2)
	}
	w.dpi = 96
	if pGetDpiForSystem.Find() == nil {
		if d, _, _ := pGetDpiForSystem.Call(); d != 0 {
			w.dpi = int32(d)
		}
	}
	w.steps, w.folderName, w.installErr = steps, filepath.Base(defaults.Dir), make(chan error, 1)
	w.boxes = map[int]windows.Handle{}
	w.font = font(9, 400, "Segoe UI")
	w.heading = font(12, 600, "Segoe UI")

	inst, _, _ := pGetModuleHandle.Call(0)
	cursor, _, _ := pLoadCursor.Call(0, 32512) // IDC_ARROW
	brush, _, _ := pGetSysColorBrush.Call(colorBtnFace)
	class := wndClassEx{
		wndProc:    windows.NewCallback(wndProc),
		instance:   windows.Handle(inst),
		cursor:     windows.Handle(cursor),
		background: windows.Handle(brush),
		className:  windows.StringToUTF16Ptr("DMSSetup"),
	}
	class.size = uint32(unsafe.Sizeof(class))
	pRegisterClassEx.Call(uintptr(unsafe.Pointer(&class)))

	const style = wsCaption | wsSysMenu | wsMinimizeBox
	cw, ch := px(width), px(height)
	r := rect{0, 0, cw, ch}
	pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&r)), style, 0, wsExControlParent)
	ww, wh := r.right-r.left, r.bottom-r.top
	sw, _, _ := pGetSystemMetrics.Call(0)
	sh, _, _ := pGetSystemMetrics.Call(1)
	main, _, _ := pCreateWindowEx.Call(wsExControlParent, uintptr(unsafe.Pointer(class.className)), str("Install downloadmanagersquared"),
		style, uintptr((int32(sw)-ww)/2), uintptr((int32(sh)-wh)/2), uintptr(ww), uintptr(wh), 0, 0, inst, 0)
	if main == 0 {
		return false
	}
	w.main = windows.Handle(main)
	if ico := iconHandle(icon); ico != 0 {
		pSendMessage.Call(main, wmSetIcon, 0, uintptr(ico))
		pSendMessage.Call(main, wmSetIcon, 1, uintptr(ico))
	}

	y := int32(margin)
	h := control("STATIC", "Downloads over all your connections at once.", 0, 0, 0, margin, y, width-2*margin, 24)
	pSendMessage.Call(uintptr(h), wmSetFont, uintptr(w.heading), 1)
	y += 36
	control("STATIC", "Install folder", 0, 0, 0, margin, y, 300, 18)
	y += 22
	w.dir = control("EDIT", defaults.Dir, idDir, wsTabStop|wsGroup|esAutoHScroll, wsExClientEdge, margin, y, width-2*margin-96, 25)
	control("BUTTON", "Browse…", idBrowse, wsTabStop, 0, width-margin-88, y, 88, 25)
	y += 31
	w.grays = append(w.grays, control("STATIC", "If the folder needs administrator rights, Windows asks for them once.", idNote, 0, 0, margin, y, width-2*margin, 18))
	y += 32
	for _, b := range []struct {
		id   int
		text string
		on   bool
	}{
		{idStartMenu, "Add to the Start menu", defaults.StartMenu},
		{idDesktop, "Add a shortcut on the desktop", defaults.Desktop},
		{idAutostart, "Start with Windows (runs in the tray, next to the clock)", defaults.Autostart},
	} {
		style := uint32(wsTabStop | bsAutoCheckbox)
		if b.id == idStartMenu {
			style |= wsGroup
		}
		c := control("BUTTON", b.text, b.id, style, 0, margin, y, width-2*margin, 22)
		if b.on {
			pSendMessage.Call(uintptr(c), bmSetCheck, 1, 0)
		}
		w.boxes[b.id] = c
		y += 26
	}
	y = height - margin - 30
	w.install = control("BUTTON", "Install", idOK, wsTabStop|wsGroup|bsDefPushButton, 0, width-margin-2*96-8, y, 96, 30)
	control("BUTTON", "Cancel", idCancel, wsTabStop, 0, width-margin-96, y, 96, 30)

	pShowWindow.Call(main, 1) // SW_SHOWNORMAL
	pSetForeground.Call(main)
	pSetFocus.Call(uintptr(w.dir))
	pSendMessage.Call(uintptr(w.dir), emSetSel, 0, ^uintptr(0))

	var mm msg
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&mm)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		if r, _, _ := pIsDialogMessage.Call(main, uintptr(unsafe.Pointer(&mm))); r != 0 {
			continue
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&mm)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&mm)))
	}
	return w.installed
}

// The window's client size and margin in 96-DPI pixels.
const width, height, margin = 460, 280, 16

// control creates a child of the main window on the page showing, laid out in
// 96-DPI pixels.
func control(class, text string, id int, style, exStyle uint32, x, y, cx, cy int32) windows.Handle {
	h, _, _ := pCreateWindowEx.Call(uintptr(exStyle), str(class), str(text), uintptr(wsChild|wsVisible|style),
		uintptr(px(x)), uintptr(px(y)), uintptr(px(cx)), uintptr(px(cy)), uintptr(w.main), uintptr(id), 0, 0)
	pSendMessage.Call(h, wmSetFont, uintptr(w.font), 1)
	w.page = append(w.page, windows.Handle(h))
	return windows.Handle(h)
}

// startInstall installs what the window shows, off its thread so it keeps
// painting (and Windows' administrator prompt can come up over it).
func startInstall(c Choice) {
	w.installing = true
	for _, h := range w.page {
		pEnableWindow.Call(uintptr(h), 0)
	}
	pSetWindowText.Call(uintptr(w.install), str("Installing…"))
	go func() {
		w.installErr <- w.steps.Install(c)
		pPostMessage.Call(uintptr(w.main), wmInstalled, 0, 0)
	}()
}

// installDone shows the finished page, or brings the first page back after
// saying what went wrong.
func installDone() {
	w.installing = false
	if err := <-w.installErr; err != nil {
		for _, h := range w.page {
			pEnableWindow.Call(uintptr(h), 1)
		}
		pSetWindowText.Call(uintptr(w.install), str("Install"))
		if !errors.Is(err, ErrReported) {
			windows.MessageBox(windows.HWND(w.main), windows.StringToUTF16Ptr("Could not install: "+err.Error()),
				windows.StringToUTF16Ptr("Install downloadmanagersquared"), windows.MB_OK|windows.MB_ICONERROR)
		}
		return
	}
	w.installed = true
	for _, h := range w.page {
		pDestroyWindow.Call(uintptr(h))
	}
	w.page, w.grays = nil, nil
	pSetWindowText.Call(uintptr(w.main), str("downloadmanagersquared is installed"))

	y := int32(margin)
	h := control("STATIC", "DMS² is installed.", 0, 0, 0, margin, y, width-2*margin, 24)
	pSendMessage.Call(uintptr(h), wmSetFont, uintptr(w.heading), 1)
	y += 36
	control("STATIC", "It runs in the tray, next to the clock. One step left: add the Firefox extension, which takes over your downloads and shows them.", 0, 0, 0, margin, y, width-2*margin, 36)
	y += 48
	add := control("BUTTON", "Add to Firefox", idAddExtension, wsTabStop|wsGroup|bsDefPushButton, 0, margin, y, 140, 30)
	y += 48
	w.grays = append(w.grays, control("STATIC", "Using LibreWolf, Waterfox or Floorp? Open this link in that browser instead:", 0, 0, 0, margin, y, width-2*margin, 18))
	y += 22
	control("EDIT", w.steps.ExtensionURL, idURL, wsTabStop|esAutoHScroll|esReadOnly, wsExClientEdge, margin, y, width-2*margin, 25)
	control("BUTTON", "Close", idCancel, wsTabStop, 0, width-margin-96, height-margin-30, 96, 30)
	pSetFocus.Call(uintptr(add))
}

func font(points, weight int32, face string) windows.Handle {
	h, _, _ := pCreateFont.Call(uintptr(-points*w.dpi/72), 0, 0, 0, uintptr(weight), 0, 0, 0,
		1 /* DEFAULT_CHARSET */, 0, 0, 5 /* CLEARTYPE_QUALITY */, 0, str(face))
	return windows.Handle(h)
}

func text(h windows.Handle) string {
	n, _, _ := pGetWindowTextLength.Call(uintptr(h))
	buf := make([]uint16, n+1)
	pGetWindowText.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), n+1)
	return windows.UTF16ToString(buf)
}

func wndProc(hwnd windows.Handle, m uint32, wParam, lParam uintptr) uintptr {
	switch m {
	case wmCommand:
		switch int(wParam & 0xffff) {
		case idBrowse:
			browse()
		case idOK:
			if w.installed || w.installing {
				break
			}
			if dir, ok := validate(text(w.dir)); ok {
				startInstall(Choice{
					Dir:       dir,
					StartMenu: checked(idStartMenu),
					Desktop:   checked(idDesktop),
					Autostart: checked(idAutostart),
				})
			}
		case idAddExtension:
			w.steps.AddExtension()
		case idCancel:
			if !w.installing {
				pDestroyWindow.Call(uintptr(hwnd))
			}
		}
		return 0
	case wmInstalled:
		installDone()
		return 0
	case wmCtlColorStatic:
		if slices.Contains(w.grays, windows.Handle(lParam)) {
			gray, _, _ := pGetSysColor.Call(colorGrayText)
			pSetTextColor.Call(wParam, gray)
			pSetBkMode.Call(wParam, 1) // TRANSPARENT
			b, _, _ := pGetSysColorBrush.Call(colorBtnFace)
			return b
		}
	case wmClose:
		if !w.installing {
			pDestroyWindow.Call(uintptr(hwnd))
		}
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProc.Call(uintptr(hwnd), uintptr(m), wParam, lParam)
	return r
}

func checked(id int) bool {
	r, _, _ := pSendMessage.Call(uintptr(w.boxes[id]), bmGetCheck, 0, 0)
	return r == 1
}

// browse picks a folder and puts the program's own folder below it, as
// installers do, unless the user picked a folder of that name.
func browse() {
	start := existing(strings.TrimSpace(text(w.dir)))
	p, ok := winutil.PickFolder(w.main, start, "Choose where to install DMS²")
	if !ok {
		return
	}
	if !strings.EqualFold(filepath.Base(p), w.folderName) {
		p = filepath.Join(p, w.folderName)
	}
	pSetWindowText.Call(uintptr(w.dir), str(p))
}

// existing is dir, or the nearest folder above it that exists.
func existing(dir string) string {
	for dir != "" {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
		up := filepath.Dir(dir)
		if up == dir {
			return ""
		}
		dir = up
	}
	return ""
}

// validate checks the folder typed in, telling the user what's wrong.
func validate(dir string) (string, bool) {
	dir, problem := checkDir(dir)
	if problem != "" {
		windows.MessageBox(windows.HWND(w.main), windows.StringToUTF16Ptr(problem), windows.StringToUTF16Ptr("Install downloadmanagersquared"), windows.MB_OK|windows.MB_ICONWARNING)
		pSetFocus.Call(uintptr(w.dir))
		return "", false
	}
	return dir, true
}

// checkDir cleans the folder typed in, or says why it can't be installed to.
func checkDir(dir string) (string, string) {
	dir = strings.Trim(strings.TrimSpace(dir), `"`)
	if dir == "" || !filepath.IsAbs(dir) || filepath.VolumeName(dir) == "" {
		return "", `Enter a full path, such as C:\Program Files\` + w.folderName + "."
	}
	dir = filepath.Clean(dir)
	if filepath.Dir(dir) == dir {
		return "", "Choose a folder, not a whole drive."
	}
	if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
		return "", dir + " is a file, not a folder."
	}
	return dir, ""
}

func iconHandle(ico []byte) windows.Handle {
	if len(ico) < 22 {
		return 0
	}
	off := int(uint32(ico[18]) | uint32(ico[19])<<8 | uint32(ico[20])<<16 | uint32(ico[21])<<24)
	if off >= len(ico) {
		return 0
	}
	img := ico[off:]
	h, _, _ := pCreateIconFromRes.Call(uintptr(unsafe.Pointer(&img[0])), uintptr(len(img)), 1, 0x00030000, 0, 0, 0)
	return windows.Handle(h)
}

// modernControls makes this thread's windows use version 6 of the common
// controls (themed buttons, checkboxes and edits) rather than the Windows 95
// look an exe without a manifest gets: it activates a manifest asking for
// them, then loads the library under it.
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
