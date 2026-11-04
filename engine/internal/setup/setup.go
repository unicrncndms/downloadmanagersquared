// Package setup is the window a first install shows: where to install and
// what to set up besides the engine, then, once installed, the one step left
// (adding the browser extension). It is Win32, drawn in the extension's
// colours and following Windows' light or dark mode.
package setup

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
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
	Install      func(Choice) error       // runs off the window's thread
	Browsers     func() []winutil.Browser // the installed browsers the extension runs in
	AddExtension func(winutil.Browser)    // opens the extension in that browser to add
	GetFirefox   func()                   // opens Firefox's download page, when there is no browser for it
	Uninstall    func()                   // removes the install again, when there is no browser for it
}

// ErrReported is an Install failure the user was already told about.
var ErrReported = errors.New("already reported")

// Control IDs. IDOK and IDCANCEL are what Enter and Esc send.
const (
	idOK         = 1
	idCancel     = 2
	idBrowse     = 100
	idDir        = 101
	idStartMenu  = 102
	idDesktop    = 103
	idAutostart  = 104
	idGetFirefox = 105
	idUninstall  = 106
	idBrowser    = 200 // + the browser's index

	timerHover = 1

	wmActivate = 0x0006
)

// Layout, in 96-DPI pixels.
const (
	width, height = 520, 364
	pad           = 24
	footer        = 64 // the band with the buttons at the bottom
)

type kind int

const (
	button kind = iota
	primary
	checkbox
)

// ctl is how to draw one of the window's buttons.
type ctl struct {
	kind    kind
	checked bool
	footer  bool // sits on the footer band
}

type label struct {
	r     rect
	text  string
	font  windows.Handle
	color func() uint32
	flags uintptr
}

type field struct {
	r    rect
	edit windows.Handle
}

// The window's state; there is only ever one.
var w struct {
	main                                      windows.Handle
	dir, install                              windows.Handle
	browsers                                  []winutil.Browser // on the finished page
	children                                  []windows.Handle
	ctls                                      map[windows.Handle]*ctl
	labels                                    []label
	fields                                    []field
	note                                      int // index of the label under the folder field
	hover                                     windows.Handle
	done                                      bool // on the finished page
	icon                                      windows.Handle
	pal                                       palette
	dark                                      bool
	brushPanel, brushBg                       windows.Handle
	fontBody, fontSmall, fontLabel, fontTitle windows.Handle
	steps                                     Steps
	installing                                bool
	installErr                                chan error
	installed                                 bool
	folderName                                string
	dpi                                       int32
}

func px(v int32) int32 { return v * w.dpi / 96 }

func scaled(r rect) rect { return rect{px(r.left), px(r.top), px(r.right), px(r.bottom)} }

// Run shows the window until the user closes it, and says whether it
// installed. defaults fills it in; the last part of defaults.Dir is the folder
// Browse… adds to what the user picks. icon is ICO bytes holding one PNG image.
func Run(defaults Choice, icon []byte, steps Steps) bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	modernControls()
	startGDIPlus()
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
	w.ctls = map[windows.Handle]*ctl{}
	w.fontBody, w.fontSmall = newFont(14, 400), newFont(13, 400)
	w.fontLabel, w.fontTitle = newFont(13, 600), newFont(20, 600)
	w.icon = iconHandle(icon, px(32))
	applyTheme()

	inst, _, _ := pGetModuleHandle.Call(0)
	cursor, _, _ := pLoadCursor.Call(0, 32512) // IDC_ARROW
	class := wndClassEx{
		wndProc:   windows.NewCallback(wndProc),
		instance:  windows.Handle(inst),
		cursor:    windows.Handle(cursor),
		className: windows.StringToUTF16Ptr("DMSSetup"),
	}
	class.size = uint32(unsafe.Sizeof(class))
	pRegisterClassEx.Call(uintptr(unsafe.Pointer(&class)))

	const style = wsCaption | wsSysMenu | wsMinimizeBox
	r := rect{0, 0, px(width), px(height)}
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
	applyTheme() // again, for the title bar now there is one
	if small := iconHandle(icon, int32(16*w.dpi/96)); small != 0 {
		pSendMessage.Call(main, wmSetIcon, 0, uintptr(small))
	}
	if w.icon != 0 {
		pSendMessage.Call(main, wmSetIcon, 1, uintptr(w.icon))
	}
	choosePage(defaults)
	pShowWindow.Call(main, 1) // SW_SHOWNORMAL
	pSetForeground.Call(main)

	var m msg
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		if r, _, _ := pIsDialogMessage.Call(main, uintptr(unsafe.Pointer(&m))); r != 0 {
			continue
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
	return w.installed
}

// --- pages ---

// choosePage is the first page: where to install, and what else.
func choosePage(d Choice) {
	header("Install DMS²", "Downloads over all your connections at once.")
	addLabel(rect{pad, 100, width - pad, 120}, "Install folder", w.fontLabel, func() uint32 { return w.pal.text }, dtSingleLine)
	w.dir = addField(rect{pad, 124, width - pad - 100, 156}, d.Dir, idDir, false)
	addButton("Browse…", idBrowse, button, rect{width - pad - 92, 124, width - pad, 156})
	w.note = len(w.labels)
	addLabel(rect{pad, 162, width - pad, 196}, "", w.fontSmall, nil, dtWordBreak)
	setNote("If the folder needs administrator rights, Windows asks for them once.", false)
	y := int32(200)
	for _, b := range []struct {
		id   int
		text string
		on   bool
	}{
		{idStartMenu, "Add to the Start menu", d.StartMenu},
		{idDesktop, "Add a shortcut on the desktop", d.Desktop},
		{idAutostart, "Start with Windows (runs in the tray, next to the clock)", d.Autostart},
	} {
		h := addButton(b.text, b.id, checkbox, rect{pad - 2, y, width - pad, y + 28})
		w.ctls[h].checked = b.on
		y += 30
	}
	w.install = addButton("Install", idOK, primary, footerButton(1))
	addButton("Cancel", idCancel, button, footerButton(0))
	pSetFocus.Call(uintptr(w.dir))
	pSendMessage.Call(uintptr(w.dir), emSetSel, 0, ^uintptr(0))
}

// maxBrowsers is how many browsers the finished page has room for.
const maxBrowsers = 6

// donePage is the last page: the one step left, adding the extension to a
// browser. Without a browser for it, it says so and offers Firefox or
// uninstalling.
func donePage() {
	for _, h := range w.children {
		pDestroyWindow.Call(uintptr(h))
	}
	w.children, w.labels, w.fields, w.hover = nil, nil, nil, 0
	w.ctls = map[windows.Handle]*ctl{}
	w.done = true
	w.browsers = w.steps.Browsers()
	if len(w.browsers) > maxBrowsers {
		w.browsers = w.browsers[:maxBrowsers]
	}
	text := func() uint32 { return w.pal.text }
	header("DMS² is installed", "It runs in the tray, next to the clock.")
	var first windows.Handle
	if len(w.browsers) == 0 {
		addLabel(rect{pad, 100, width - pad, 160}, "DMS² works through Firefox or a browser based on it, such as LibreWolf, Waterfox or Floorp, "+
			"and none is installed. Install one, then come back to this window.", w.fontBody, text, dtWordBreak)
		first = addButton("Download Firefox", idGetFirefox, primary, rect{pad, 170, pad + 170, 202})
		addButton("Uninstall DMS²", idUninstall, button, rect{pad + 178, 170, pad + 348, 202})
	} else {
		addLabel(rect{pad, 100, width - pad, 140}, "One step left: add the DMS² extension to your browser. It takes over your downloads and shows them.",
			w.fontBody, text, dtWordBreak)
		const bw, gap = (width - 2*pad - 8) / 2, 8
		y := int32(146)
		for i, b := range w.browsers {
			x := int32(pad + (i%2)*(bw+gap))
			k := button
			if i == 0 {
				k = primary
			}
			h := addButton("Add to "+b.Name, idBrowser+i, k, rect{x, y, x + bw, y + 32})
			if i == 0 {
				first = h
			}
			if i%2 == 1 || i == len(w.browsers)-1 {
				y += 40
			}
		}
		addLabel(rect{pad, y + 2, width - pad, y + 22}, "The browser asks once whether to add it.",
			w.fontSmall, func() uint32 { return w.pal.muted }, dtSingleLine)
	}
	addButton("Close", idCancel, button, footerButton(0))
	invalidate(w.main)
	pSetFocus.Call(uintptr(first))
}

func header(title, sub string) {
	addLabel(rect{68, 22, width - pad, 50}, title, w.fontTitle, func() uint32 { return w.pal.text }, dtSingleLine)
	addLabel(rect{68, 50, width - pad, 72}, sub, w.fontBody, func() uint32 { return w.pal.muted }, dtSingleLine|dtEndEllipsis)
}

// footerButton is the place of the i-th button from the right in the footer.
func footerButton(i int32) rect {
	right := width - pad - i*(100+8)
	top := int32(height - footer + 16)
	return rect{right - 100, top, right, top + 32}
}

func addLabel(r rect, text string, font windows.Handle, color func() uint32, flags uintptr) {
	w.labels = append(w.labels, label{r, text, font, color, flags})
}

func setNote(s string, bad bool) {
	l := &w.labels[w.note]
	l.text = s
	l.color = func() uint32 { return w.pal.muted }
	if bad {
		l.color = func() uint32 { return w.pal.bad }
	}
	invalidate(w.main)
}

func child(class, text string, id int, style uint32, r rect) windows.Handle {
	r = scaled(r)
	h, _, _ := pCreateWindowEx.Call(0, str(class), str(text), uintptr(wsChild|wsVisible|style),
		uintptr(r.left), uintptr(r.top), uintptr(r.right-r.left), uintptr(r.bottom-r.top), uintptr(w.main), uintptr(id), 0, 0)
	pSendMessage.Call(h, wmSetFont, uintptr(w.fontBody), 1)
	w.children = append(w.children, windows.Handle(h))
	return windows.Handle(h)
}

func addButton(text string, id int, k kind, r rect) windows.Handle {
	style := uint32(wsTabStop | bsOwnerDraw)
	if k != checkbox || id == idStartMenu {
		style |= wsGroup
	}
	h := child("BUTTON", text, id, style, r)
	w.ctls[h] = &ctl{kind: k, footer: r.top >= height-footer}
	return h
}

// addField is a text field: an edit control without a border inside a box
// the window draws.
func addField(r rect, text string, id int, readOnly bool) windows.Handle {
	style := uint32(wsTabStop | wsGroup | esAutoHScroll)
	if readOnly {
		style |= esReadOnly
	}
	h := child("EDIT", text, id, style, rect{r.left + 10, r.top + 6, r.right - 10, r.bottom - 6})
	w.fields = append(w.fields, field{r, h})
	return h
}

// --- painting ---

func paint(c canvas) {
	full := rect{0, 0, px(width), px(height)}
	split := px(height - footer)
	c.fill(rect{0, 0, full.right, split}, w.pal.panel)
	c.fill(rect{0, split, full.right, full.bottom}, w.pal.bg)
	c.fill(rect{0, split, full.right, split + max(1, px(1))}, w.pal.line)

	if w.done {
		c.ellipse(float32(px(pad)), float32(px(26)), float32(px(32)), float32(px(32)), w.pal.ok)
		c.check(float32(px(pad)), float32(px(26)), float32(px(32)), float32(px(3)), w.pal.panel,
			[3][2]float32{{0.28, 0.52}, {0.44, 0.67}, {0.72, 0.36}})
	} else if w.icon != 0 {
		pDrawIconEx.Call(uintptr(c.hdc), uintptr(px(pad)), uintptr(px(26)), uintptr(w.icon), uintptr(px(32)), uintptr(px(32)), 0, 0, 3 /* DI_NORMAL */)
	}
	for _, l := range w.labels {
		c.text(l.text, scaled(l.r), l.font, l.color(), l.flags)
	}
	focus, _, _ := pGetFocus.Call()
	for _, fl := range w.fields {
		r := scaled(fl.r)
		stroke, sw := w.pal.line, float32(max(1, px(1)))
		if windows.Handle(focus) == fl.edit {
			stroke, sw = w.pal.accent, float32(px(2))
		}
		c.roundRect(float32(r.left), float32(r.top), float32(r.right-r.left), float32(r.bottom-r.top), float32(px(6)), w.pal.panel, stroke, sw)
	}
}

func drawCtl(c canvas, d *drawItemStruct, k *ctl) {
	r := d.item
	wd, ht := float32(r.right-r.left), float32(r.bottom-r.top)
	bg := w.pal.panel
	if k.footer {
		bg = w.pal.bg
	}
	c.fill(r, bg)
	disabled := d.itemState&odsDisabled != 0
	pressed := d.itemState&odsSelected != 0
	focused := d.itemState&odsFocus != 0 && d.itemState&odsNoFocusRect == 0
	hover := w.hover == d.hwndItem && !disabled
	radius, one := float32(px(6)), float32(max(1, px(1)))

	switch k.kind {
	case checkbox:
		box := float32(px(18))
		x, y := float32(px(3)), (ht-box)/2
		if focused {
			c.roundRect(x-2, y-2, box+4, box+4, float32(px(5)), bg, w.pal.accent, float32(px(2)))
		}
		switch {
		case k.checked:
			fill := w.pal.accent
			if disabled {
				fill = w.pal.muted
			} else if hover {
				fill = shade(fill, 0.12)
			}
			c.roundRect(x, y, box, box, float32(px(4)), fill, 0, 0)
			c.check(x, y, box, float32(px(2)), 0xffffff, [3][2]float32{{0.24, 0.52}, {0.42, 0.7}, {0.76, 0.3}})
		default:
			border := mix(w.pal.muted, w.pal.panel, 0.3)
			if hover {
				border = w.pal.accent
			}
			c.roundRect(x, y, box, box, float32(px(4)), w.pal.panel, border, one)
		}
		color := w.pal.text
		if disabled {
			color = w.pal.muted
		}
		c.text(text(d.hwndItem), rect{px(32), 0, r.right, r.bottom}, w.fontBody, color, dtSingleLine|dtVCenter|dtEndEllipsis)
		return

	case primary:
		fill, color := w.pal.accent, uint32(0xffffff)
		switch {
		case disabled:
			fill, color = w.pal.line, w.pal.muted
		case pressed:
			fill = shade(fill, 0.22)
		case hover:
			fill = shade(fill, 0.12)
		}
		if focused {
			c.roundRect(0, 0, wd, ht, radius, fill, w.pal.text, float32(px(2)))
		} else {
			c.roundRect(0, 0, wd, ht, radius, fill, 0, 0)
		}
		c.text(text(d.hwndItem), rect{0, 0, r.right, r.bottom}, w.fontBody, color, dtSingleLine|dtVCenter|dtCenter|dtEndEllipsis)

	default:
		fill, border, color := w.pal.panel, w.pal.line, w.pal.text
		switch {
		case disabled:
			color = w.pal.muted
		case pressed:
			fill, border = w.pal.accentSoft, w.pal.accent
		case hover:
			border = w.pal.accent
		}
		sw := one
		if focused {
			border, sw = w.pal.accent, float32(px(2))
		}
		c.roundRect(0, 0, wd, ht, radius, fill, border, sw)
		c.text(text(d.hwndItem), rect{0, 0, r.right, r.bottom}, w.fontBody, color, dtSingleLine|dtVCenter|dtCenter|dtEndEllipsis)
	}
}

// shade darkens a colour on a light theme and lightens it on a dark one.
func shade(c uint32, t float64) uint32 {
	if w.dark {
		return mix(c, 0xffffff, t)
	}
	return mix(c, 0x000000, t)
}

// setHover marks the button under the mouse, and watches for the mouse
// leaving it (Windows doesn't tell the window when it leaves a child).
func setHover(h windows.Handle) {
	if h == w.hover {
		return
	}
	if w.hover != 0 {
		invalidate(w.hover)
	}
	w.hover = h
	if h != 0 {
		invalidate(h)
		pSetTimer.Call(uintptr(w.main), timerHover, 50, 0)
	}
}

// --- behaviour ---

func wndProc(hwnd windows.Handle, m uint32, wParam, lParam uintptr) uintptr {
	switch m {
	case wmCommand:
		command(int(wParam&0xffff), uint32(wParam>>16))
		return 0
	case wmDrawItem:
		d := *(**drawItemStruct)(unsafe.Pointer(&lParam))
		if k := w.ctls[d.hwndItem]; k != nil {
			buffered(d.hdc, d.item, func(c canvas) { drawCtl(c, d, k) })
		}
		return 1
	case wmPaint:
		var ps paintStruct
		hdc, _, _ := pBeginPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		var r rect
		pGetClientRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&r)))
		buffered(windows.Handle(hdc), r, paint)
		pEndPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		return 0
	case wmEraseBkgnd:
		return 1
	case wmCtlColorEdit, wmCtlColorStatic: // the text fields
		color := w.pal.text
		if !enabled(windows.Handle(lParam)) {
			color = w.pal.muted
		}
		pSetTextColor.Call(wParam, colorref(color))
		pSetBkColor.Call(wParam, colorref(w.pal.panel))
		return uintptr(w.brushPanel)
	case wmSetCursor:
		if _, ok := w.ctls[windows.Handle(wParam)]; ok {
			setHover(windows.Handle(wParam))
		} else {
			setHover(0)
		}
	case wmTimer:
		switch wParam {
		case timerHover:
			var pt [2]int32
			pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
			under, _, _ := pWindowFromPoint.Call(uintptr(uint32(pt[0])) | uintptr(uint32(pt[1]))<<32)
			if windows.Handle(under) != w.hover {
				pKillTimer.Call(uintptr(hwnd), timerHover)
				setHover(0)
			}
		}
		return 0
	case wmActivate: // back from installing a browser, maybe
		if wParam&0xffff != 0 && w.done && !slices.Equal(w.steps.Browsers(), w.browsers) {
			donePage()
		}
	case wmSettingChange: // maybe light or dark mode changed
		applyTheme()
		invalidate(w.main)
		for _, h := range w.children {
			invalidate(h)
		}
	case wmInstalled:
		installDone()
		return 0
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

func command(id int, code uint32) {
	if id == idDir {
		if code == enSetFocus || code == enKillFocus {
			invalidate(w.main) // the field's outline
		}
		return
	}
	if i := id - idBrowser; i >= 0 && i < len(w.browsers) {
		w.steps.AddExtension(w.browsers[i])
		return
	}
	switch id {
	case idBrowse:
		browse()
	case idStartMenu, idDesktop, idAutostart:
		if h := byID(id); h != 0 {
			w.ctls[h].checked = !w.ctls[h].checked
			invalidate(h)
		}
	case idOK:
		switch {
		case w.done && len(w.browsers) > 0: // Enter: the first browser
			w.steps.AddExtension(w.browsers[0])
		case w.done:
			w.steps.GetFirefox()
		case w.installing:
		default:
			dir, problem := checkDir(text(w.dir))
			if problem != "" {
				setNote(problem, true)
				pSetFocus.Call(uintptr(w.dir))
				return
			}
			startInstall(Choice{Dir: dir, StartMenu: checked(idStartMenu), Desktop: checked(idDesktop), Autostart: checked(idAutostart)})
		}
	case idGetFirefox:
		w.steps.GetFirefox()
	case idUninstall:
		w.steps.Uninstall() // it asks for itself, in its own windows
		pDestroyWindow.Call(uintptr(w.main))
	case idCancel:
		if !w.installing {
			pDestroyWindow.Call(uintptr(w.main))
		}
	}
}

func byID(id int) windows.Handle {
	for _, h := range w.children {
		if r, _, _ := pGetDlgCtrlID.Call(uintptr(h)); int(r) == id {
			return h
		}
	}
	return 0
}

var (
	pGetDlgCtrlID = user32.NewProc("GetDlgCtrlID")
	pGetFocus     = user32.NewProc("GetFocus")
)

func checked(id int) bool {
	h := byID(id)
	return h != 0 && w.ctls[h].checked
}

// startInstall installs what the window shows, off its thread so it keeps
// painting (and Windows' administrator prompt can come up over it).
func startInstall(c Choice) {
	w.installing = true
	for _, h := range w.children {
		pEnableWindow.Call(uintptr(h), 0)
	}
	pSetWindowText.Call(uintptr(w.install), str("Installing…"))
	setNote("Installing…", false)
	go func() {
		w.installErr <- w.steps.Install(c)
		pPostMessage.Call(uintptr(w.main), wmInstalled, 0, 0)
	}()
}

// installDone shows the finished page, or brings the first page back saying
// what went wrong.
func installDone() {
	w.installing = false
	if err := <-w.installErr; err != nil {
		for _, h := range w.children {
			pEnableWindow.Call(uintptr(h), 1)
		}
		pSetWindowText.Call(uintptr(w.install), str("Install"))
		if errors.Is(err, ErrReported) {
			setNote("Not installed.", true)
		} else {
			setNote("Could not install: "+err.Error(), true)
		}
		return
	}
	w.installed = true
	donePage()
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
	setNote("If the folder needs administrator rights, Windows asks for them once.", false)
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
