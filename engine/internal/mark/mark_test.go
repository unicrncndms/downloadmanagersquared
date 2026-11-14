package mark

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// At every size the mark is the same left to right, and down its middle lie
// the tile, the violet chevron's point and the teal one's.
func TestEverySizeIsTheSameDrawing(t *testing.T) {
	for _, n := range Sizes {
		img := Draw(n, nil)
		for y := range n {
			for x := range n / 2 {
				// A sample on an edge may round either way: one of 64 is 4 of 255.
				if a, b := img.NRGBAAt(x, y), img.NRGBAAt(n-1-x, y); max(a.A, b.A)-min(a.A, b.A) > 4 {
					t.Fatalf("%d: not mirrored at %d,%d", n, x, y)
				}
			}
		}
		at := func(y float64) color.NRGBA { return img.NRGBAAt(n/2, int(y*float64(n)/16)) }
		if at(2) != rgb(tileHex) || at(violetY) != rgb(violet) || at(tealY) != rgb(teal) {
			t.Errorf("%d: middle column is %v, %v, %v", n, at(2), at(violetY), at(tealY))
		}
		if img.NRGBAAt(0, 0).A != 0 {
			t.Errorf("%d: the tile's corner is not rounded", n)
		}
	}
}

func TestIcoHoldsEveryImage(t *testing.T) {
	b := ICO(Entry{16, []byte("ab")}, Entry{256, []byte("cde")})
	if len(b) != 6+32+5 || b[4] != 2 || b[6] != 16 || b[22] != 0 {
		t.Fatalf("bad ico % x", b)
	}
}

// MARK_SHEET=<file.png> go test ./internal/mark writes a contact sheet: every
// size at 1:1 on light and dark, and enlarged 8x, with and without a status dot.
func TestContactSheet(t *testing.T) {
	path := os.Getenv("MARK_SHEET")
	if path == "" {
		t.Skip("MARK_SHEET not set")
	}
	const pad = 12
	rowW := pad
	for _, n := range Sizes {
		rowW += n + pad
	}
	rowW = max(rowW, 8*(16+20+24+32+40+48)+7*pad)
	sheet := image.NewNRGBA(image.Rect(0, 0, rowW, 2*(4*pad+256+96+384)))
	fill := func(r image.Rectangle, c color.NRGBA) {
		draw.Draw(sheet, r, image.NewUniform(c), image.Point{}, draw.Src)
	}
	place := func(n, x, y, scale int, dot *color.NRGBA) {
		src := Draw(n, dot)
		for yy := range n {
			for xx := range n {
				c := src.NRGBAAt(xx, yy)
				for dy := range scale {
					for dx := range scale {
						sheet.Set(x+xx*scale+dx, y+yy*scale+dy, over(sheet.NRGBAAt(x+xx*scale+dx, y+yy*scale+dy), c))
					}
				}
			}
		}
	}
	green := &color.NRGBA{0x16, 0xa3, 0x4a, 255}
	light, drk := color.NRGBA{0xf3, 0xf3, 0xf3, 255}, color.NRGBA{0x20, 0x20, 0x20, 255}
	half := sheet.Rect.Dy() / 2
	fill(sheet.Rect, light)
	fill(image.Rect(0, half, rowW, sheet.Rect.Dy()), drk)
	for i := range 2 {
		y := i*half + pad
		x := pad
		for _, n := range Sizes {
			place(n, x, y, 1, nil)
			if n < 256 {
				place(n, x, y+256+pad, 1, green)
			}
			x += n + pad
		}
		x, y = pad, y+256+pad+96+pad
		for _, n := range []int{16, 20, 24, 32, 40, 48} {
			place(n, x, y, 8, nil)
			x += n*8 + pad
		}
	}
	f, err := os.Create(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, sheet)
}

// over is s drawn on d.
func over(d, s color.NRGBA) color.NRGBA {
	a := float64(s.A) / 255
	m := func(sv, dv uint8) uint8 { return uint8(float64(sv)*a + float64(dv)*(1-a)) }
	return color.NRGBA{m(s.R, d.R), m(s.G, d.G), m(s.B, d.B), 255}
}
