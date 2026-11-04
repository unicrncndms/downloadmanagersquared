package setup

import (
	"math"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The window is drawn in the colours of the extension's ui.css, light or dark
// as Windows is set, since Windows' own buttons and checkboxes have no dark
// look. Shapes go through GDI+ for smooth corners; text through GDI.

// palette holds ui.css's colours as 0xRRGGBB.
type palette struct {
	bg, panel, text, muted, line, accent, accentSoft, ok, bad uint32
}

var (
	lightPalette = palette{0xf6f7f9, 0xffffff, 0x16181d, 0x646b78, 0xe2e5ea, 0x2563eb, 0xe6eefe, 0x15803d, 0xb91c1c}
	darkPalette  = palette{0x15171b, 0x1d2026, 0xe8eaee, 0x9aa1ad, 0x2c3038, 0x5b8cff, 0x1f2a44, 0x4ade80, 0xf87171}
)

// forceTheme ("light" or "dark") overrides Windows' setting, for looking at
// both: go build -ldflags "-X …/internal/setup.forceTheme=dark".
var forceTheme string

// appsDark is whether Windows is set to dark mode for apps.
func appsDark() bool {
	if forceTheme != "" {
		return forceTheme == "dark"
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

func colorref(c uint32) uintptr { return uintptr(c>>16&0xff | c&0xff00 | (c&0xff)<<16) }
func argb(c uint32) uintptr     { return uintptr(0xff000000 | c) }

// mix is a blended toward b by t (0..1).
func mix(a, b uint32, t float64) uint32 {
	var out uint32
	for shift := 0; shift <= 16; shift += 8 {
		x, y := float64(a>>shift&0xff), float64(b>>shift&0xff)
		out |= uint32(math.Round(x+(y-x)*t)) << shift
	}
	return out
}

// applyTheme picks the palette and makes the title bar match it (dark title
// bar on Windows 10 and 11; on 11 also its colours).
func applyTheme() {
	w.dark = appsDark()
	w.pal = lightPalette
	if w.dark {
		w.pal = darkPalette
	}
	for _, b := range []windows.Handle{w.brushPanel, w.brushBg} {
		if b != 0 {
			pDeleteObject.Call(uintptr(b))
		}
	}
	b, _, _ := pCreateSolidBrush.Call(colorref(w.pal.panel))
	w.brushPanel = windows.Handle(b)
	b, _, _ = pCreateSolidBrush.Call(colorref(w.pal.bg))
	w.brushBg = windows.Handle(b)
	if w.main == 0 || pDwmSetWindowAttribute.Find() != nil {
		return
	}
	dark := uint32(0)
	if w.dark {
		dark = 1
	}
	caption, captionText := uint32(colorref(w.pal.panel)), uint32(colorref(w.pal.text))
	for _, a := range []struct {
		attr uintptr
		v    *uint32
	}{{20, &dark}, {35, &caption}, {36, &captionText}} { // immersive dark mode, caption colour, caption text colour
		pDwmSetWindowAttribute.Call(uintptr(w.main), a.attr, uintptr(unsafe.Pointer(a.v)), 4)
	}
}

var (
	gdiplus               = windows.NewLazySystemDLL("gdiplus.dll")
	pGdiplusStartup       = gdiplus.NewProc("GdiplusStartup")
	pGdipCreateFromHDC    = gdiplus.NewProc("GdipCreateFromHDC")
	pGdipDeleteGraphics   = gdiplus.NewProc("GdipDeleteGraphics")
	pGdipSetSmoothingMode = gdiplus.NewProc("GdipSetSmoothingMode")
	pGdipCreatePath       = gdiplus.NewProc("GdipCreatePath")
	pGdipDeletePath       = gdiplus.NewProc("GdipDeletePath")
	pGdipAddPathArc       = gdiplus.NewProc("GdipAddPathArc")
	pGdipClosePathFigure  = gdiplus.NewProc("GdipClosePathFigure")
	pGdipCreateSolidFill  = gdiplus.NewProc("GdipCreateSolidFill")
	pGdipDeleteBrush      = gdiplus.NewProc("GdipDeleteBrush")
	pGdipCreatePen1       = gdiplus.NewProc("GdipCreatePen1")
	pGdipDeletePen        = gdiplus.NewProc("GdipDeletePen")
	pGdipSetPenLineJoin   = gdiplus.NewProc("GdipSetPenLineJoin")
	pGdipSetPenStartCap   = gdiplus.NewProc("GdipSetPenStartCap")
	pGdipSetPenEndCap     = gdiplus.NewProc("GdipSetPenEndCap")
	pGdipFillPath         = gdiplus.NewProc("GdipFillPath")
	pGdipDrawPath         = gdiplus.NewProc("GdipDrawPath")
	pGdipFillEllipse      = gdiplus.NewProc("GdipFillEllipse")
	pGdipDrawLines        = gdiplus.NewProc("GdipDrawLines")
)

// f passes a float32 to GDI+: Go's Windows calls hand each argument over in
// both integer and float registers, so its bits are enough.
func f(v float32) uintptr { return uintptr(math.Float32bits(v)) }

func startGDIPlus() {
	input := struct {
		version                  uint32
		callback                 uintptr
		suppressBackgroundThread int32
		suppressExternalCodecs   int32
	}{version: 1}
	var token uintptr
	pGdiplusStartup.Call(uintptr(unsafe.Pointer(&token)), uintptr(unsafe.Pointer(&input)), 0)
}

// canvas draws on a DC in 96-DPI pixels.
type canvas struct {
	hdc windows.Handle
	g   uintptr
}

func newCanvas(hdc windows.Handle) canvas {
	var g uintptr
	pGdipCreateFromHDC.Call(uintptr(hdc), uintptr(unsafe.Pointer(&g)))
	pGdipSetSmoothingMode.Call(g, 4) // anti-alias
	return canvas{hdc, g}
}

func (c canvas) close() { pGdipDeleteGraphics.Call(c.g) }

func (c canvas) fill(r rect, color uint32) {
	b, _, _ := pCreateSolidBrush.Call(colorref(color))
	pFillRect.Call(uintptr(c.hdc), uintptr(unsafe.Pointer(&r)), b)
	pDeleteObject.Call(b)
}

// roundRect fills and outlines a rounded rectangle given in device pixels;
// stroke 0 draws no outline.
func (c canvas) roundRect(x, y, wd, ht, radius float32, fill, stroke uint32, strokeW float32) {
	var path uintptr
	pGdipCreatePath.Call(0, uintptr(unsafe.Pointer(&path)))
	defer pGdipDeletePath.Call(path)
	if strokeW > 0 { // keep the outline inside, on whole pixels
		x, y, wd, ht = x+strokeW/2, y+strokeW/2, wd-strokeW, ht-strokeW
	}
	d := 2 * radius
	pGdipAddPathArc.Call(path, f(x), f(y), f(d), f(d), f(180), f(90))
	pGdipAddPathArc.Call(path, f(x+wd-d), f(y), f(d), f(d), f(270), f(90))
	pGdipAddPathArc.Call(path, f(x+wd-d), f(y+ht-d), f(d), f(d), f(0), f(90))
	pGdipAddPathArc.Call(path, f(x), f(y+ht-d), f(d), f(d), f(90), f(90))
	pGdipClosePathFigure.Call(path)
	var brush uintptr
	pGdipCreateSolidFill.Call(argb(fill), uintptr(unsafe.Pointer(&brush)))
	pGdipFillPath.Call(c.g, brush, path)
	pGdipDeleteBrush.Call(brush)
	if strokeW > 0 {
		var pen uintptr
		pGdipCreatePen1.Call(argb(stroke), f(strokeW), 2 /* pixels */, uintptr(unsafe.Pointer(&pen)))
		pGdipDrawPath.Call(c.g, pen, path)
		pGdipDeletePen.Call(pen)
	}
}

func (c canvas) ellipse(x, y, wd, ht float32, color uint32) {
	var brush uintptr
	pGdipCreateSolidFill.Call(argb(color), uintptr(unsafe.Pointer(&brush)))
	pGdipFillEllipse.Call(c.g, brush, f(x), f(y), f(wd), f(ht))
	pGdipDeleteBrush.Call(brush)
}

// check draws a tick through points given as fractions of the box at x, y.
func (c canvas) check(x, y, size, width float32, color uint32, pts [3][2]float32) {
	var pen uintptr
	pGdipCreatePen1.Call(argb(color), f(width), 2, uintptr(unsafe.Pointer(&pen)))
	defer pGdipDeletePen.Call(pen)
	pGdipSetPenLineJoin.Call(pen, 2) // round
	pGdipSetPenStartCap.Call(pen, 2)
	pGdipSetPenEndCap.Call(pen, 2)
	var pf [3][2]float32
	for i, p := range pts {
		pf[i] = [2]float32{x + p[0]*size, y + p[1]*size}
	}
	pGdipDrawLines.Call(c.g, pen, uintptr(unsafe.Pointer(&pf[0])), 3)
}

// text draws s in r (device pixels) with flags (DT_…).
func (c canvas) text(s string, r rect, font windows.Handle, color uint32, flags uintptr) {
	old, _, _ := pSelectObject.Call(uintptr(c.hdc), uintptr(font))
	pSetTextColor.Call(uintptr(c.hdc), colorref(color))
	pSetBkMode.Call(uintptr(c.hdc), 1) // transparent
	pDrawText.Call(uintptr(c.hdc), str(s), ^uintptr(0), uintptr(unsafe.Pointer(&r)), flags|dtNoPrefix)
	pSelectObject.Call(uintptr(c.hdc), old)
}

// buffered paints r of hdc through an offscreen bitmap, so nothing flickers.
func buffered(hdc windows.Handle, r rect, paint func(c canvas)) {
	wd, ht := r.right-r.left, r.bottom-r.top
	mem, _, _ := pCreateCompatDC.Call(uintptr(hdc))
	bmp, _, _ := pCreateCompatBmp.Call(uintptr(hdc), uintptr(wd), uintptr(ht))
	old, _, _ := pSelectObject.Call(mem, bmp)
	c := newCanvas(windows.Handle(mem))
	paint(c)
	c.close()
	pBitBlt.Call(uintptr(hdc), uintptr(r.left), uintptr(r.top), uintptr(wd), uintptr(ht), mem, 0, 0, 0x00CC0020 /* SRCCOPY */)
	pSelectObject.Call(mem, old)
	pDeleteObject.Call(bmp)
	pDeleteDC.Call(mem)
}

func newFont(sizePx, weight int32) windows.Handle {
	h, _, _ := pCreateFont.Call(uintptr(-px(sizePx)), 0, 0, 0, uintptr(weight), 0, 0, 0,
		1 /* DEFAULT_CHARSET */, 0, 0, 5 /* CLEARTYPE_QUALITY */, 0, str("Segoe UI"))
	return windows.Handle(h)
}
