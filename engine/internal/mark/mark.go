// Package mark draws the DMS² product mark: a navy tile holding two chevrons
// that point down, one in each of two connections' colours. It is one drawing
// on a 16-unit grid, scaled to any size; edges are antialiased (supersampled).
// The extension's icons/icon.svg is the same drawing; regenerate it with
// engine/cmd/dms-engine/genicon.go.
package mark

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Sizes is every size the exe's icon and the contact sheet are built for.
var Sizes = []int{16, 20, 24, 32, 40, 48, 64, 96, 256}

// The drawing, in 16ths of the canvas: the tile's corner radius, the chevrons'
// points (y of each), half their reach sideways and their stroke width.
const (
	corner   = 4.0
	violetY  = 8.5
	tealY    = 12.5
	reach    = 4.5
	stroke   = 2.2
	tileHex  = 0x1f2a44
	violet   = 0xb197fc
	teal     = 0x4fc8dc
	svgStart = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" width="96" height="96">`
)

// SVG is the mark as an SVG: the same geometry as Draw.
const SVG = svgStart + `
  <rect width="16" height="16" rx="4" fill="#1f2a44"/>
  <path fill="none" stroke="#b197fc" stroke-width="2.2" d="M3.5 4L8 8.5 12.5 4"/>
  <path fill="none" stroke="#4fc8dc" stroke-width="2.2" d="M3.5 8L8 12.5 12.5 8"/>
</svg>
`

type shape struct {
	cover func(x, y float64) bool
	col   color.NRGBA
}

func rgb(hex uint32) color.NRGBA {
	return color.NRGBA{uint8(hex >> 16), uint8(hex >> 8), uint8(hex), 255}
}

// chevron covers a V whose point is at (8, vy): arms at 45 degrees, cut square
// at their ends, a sharp point (what an SVG stroke with mitre joins draws).
func chevron(vy float64) func(x, y float64) bool {
	return func(x, y float64) bool {
		x, y = math.Abs(x-8), y-vy
		return math.Abs(x+y) <= stroke/2*math.Sqrt2 && x-y <= 2*reach
	}
}

// shapes lists what is painted, in order, in 16ths of the canvas. px is one pixel.
func shapes(px float64, dot *color.NRGBA) []shape {
	s := []shape{
		{func(x, y float64) bool {
			cx, cy := math.Max(corner, math.Min(x, 16-corner)), math.Max(corner, math.Min(y, 16-corner))
			return math.Hypot(x-cx, y-cy) <= corner
		}, rgb(tileHex)},
		{chevron(violetY), rgb(violet)},
		{chevron(tealY), rgb(teal)},
	}
	if dot != nil { // the ringed status dot, bottom right; the ring is 1 px, 2 from 32 px up
		ring := px
		if px <= 0.5 {
			ring = 2 * px
		}
		circ := func(r float64) func(x, y float64) bool {
			return func(x, y float64) bool { return math.Hypot(x-12, y-12) <= r }
		}
		s = append(s, shape{circ(3 + ring), rgb(0xffffff)}, shape{circ(3), *dot})
	}
	return s
}

// Draw renders the mark at n x n pixels. dot, if not nil, adds a ringed status
// dot at the bottom right.
func Draw(n int, dot *color.NRGBA) *image.NRGBA {
	px := 16 / float64(n)
	sh := shapes(px, dot)
	const ss = 8
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := range n {
		for x := range n {
			var sum [3]float64
			hit := 0
			for sy := range ss {
				for sx := range ss {
					ux, uy := (float64(x)+(float64(sx)+0.5)/ss)*px, (float64(y)+(float64(sy)+0.5)/ss)*px
					var c *color.NRGBA
					for i := range sh {
						if sh[i].cover(ux, uy) {
							c = &sh[i].col
						}
					}
					if c != nil {
						sum[0], sum[1], sum[2] = sum[0]+float64(c.R), sum[1]+float64(c.G), sum[2]+float64(c.B)
						hit++
					}
				}
			}
			if hit > 0 {
				h := float64(hit)
				img.SetNRGBA(x, y, color.NRGBA{
					uint8(math.Round(sum[0] / h)), uint8(math.Round(sum[1] / h)), uint8(math.Round(sum[2] / h)),
					uint8(math.Round(255 * h / ss / ss))})
			}
		}
	}
	return img
}

// PNG is img encoded as PNG.
func PNG(img image.Image) []byte {
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// Entry is one image of an ICO: its size and PNG data.
type Entry struct {
	N   int
	PNG []byte
}

// ICO wraps PNG images (square, up to 256) as one ICO file.
func ICO(entries ...Entry) []byte {
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, []uint16{0, 1, uint16(len(entries))})
	off := 6 + 16*len(entries)
	for _, e := range entries {
		ico.Write([]byte{byte(e.N), byte(e.N), 0, 0}) // 256 wraps to 0, as the format has it
		binary.Write(&ico, binary.LittleEndian, []uint16{1, 32})
		binary.Write(&ico, binary.LittleEndian, []uint32{uint32(len(e.PNG)), uint32(off)})
		off += len(e.PNG)
	}
	for _, e := range entries {
		ico.Write(e.PNG)
	}
	return ico.Bytes()
}

// Pick is the PNG in ico best for size px: the smallest image of at least that
// size, else the largest; nil if ico holds none.
func Pick(ico []byte, size int) []byte {
	if len(ico) < 6 {
		return nil
	}
	var best []byte
	bestN := 0
	for i := range int(binary.LittleEndian.Uint16(ico[4:])) {
		e := ico[6+16*i:]
		if len(e) < 16 {
			break
		}
		n := int(e[0])
		if n == 0 {
			n = 256
		}
		off, ln := int(binary.LittleEndian.Uint32(e[12:])), int(binary.LittleEndian.Uint32(e[8:]))
		if off+ln > len(ico) {
			continue
		}
		if best == nil || (bestN < size && n > bestN) || (n >= size && n < bestN) {
			best, bestN = ico[off:off+ln], n
		}
	}
	return best
}
