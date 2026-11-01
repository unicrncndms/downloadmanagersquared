package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"sync"
)

var iconOnce = sync.OnceValue(buildIcon)

// Icon is a 32px ICO (PNG inside): a blue disc with two stacked down-arrows.
func Icon() []byte { return iconOnce() }

func buildIcon() []byte {
	const n = 32
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	blue := color.NRGBA{0x25, 0x63, 0xeb, 0xff}
	white := color.NRGBA{0xff, 0xff, 0xff, 0xff}
	c := float64(n-1) / 2
	for y := range n {
		for x := range n {
			dx, dy := float64(x)-c, float64(y)-c
			if d := math.Hypot(dx, dy); d <= c+0.5 {
				a := uint8(255 * math.Min(1, c+0.5-d))
				img.SetNRGBA(x, y, color.NRGBA{blue.R, blue.G, blue.B, a})
			}
		}
	}
	// Two chevrons pointing down: "download, squared".
	for _, top := range []int{8, 15} {
		for i := 0; i <= 7; i++ {
			for t := 0; t < 3; t++ {
				img.SetNRGBA(8+i, top+i/1+t-i/2, white)
				img.SetNRGBA(23-i, top+i/1+t-i/2, white)
			}
		}
	}
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, img)

	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, []uint16{0, 1, 1})
	ico.Write([]byte{n, n, 0, 0})
	binary.Write(&ico, binary.LittleEndian, []uint16{1, 32})
	binary.Write(&ico, binary.LittleEndian, []uint32{uint32(pngBuf.Len()), 22})
	ico.Write(pngBuf.Bytes())
	return ico.Bytes()
}
