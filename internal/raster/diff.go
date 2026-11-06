package raster

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
)

// Difference is how far apart two PNGs are.
type Difference struct {
	Mean      float64 // mean absolute difference per channel, 0-255
	Max       int     // largest difference in any channel of any pixel
	Differing float64 // share of pixels that are not identical
	SameType  bool    // same colour type and bit depth
}

// Diff compares two PNGs of the same size pixel by pixel.
func Diff(a, b []byte) (Difference, error) {
	var d Difference
	ia, err := png.Decode(bytes.NewReader(a))
	if err != nil {
		return d, err
	}
	ib, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return d, err
	}
	if ia.Bounds() != ib.Bounds() {
		return d, fmt.Errorf("sizes differ: %v and %v", ia.Bounds(), ib.Bounds())
	}
	// IHDR's bit depth and colour type are bytes 24 and 25 of the file.
	d.SameType = len(a) > 25 && len(b) > 25 && a[24] == b[24] && a[25] == b[25]
	var sum, differing int
	r := ia.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			ca, cb := rgb8(ia, x, y), rgb8(ib, x, y)
			same := true
			for c := range ca {
				v := ca[c] - cb[c]
				if v < 0 {
					v = -v
				}
				sum += v
				d.Max = max(d.Max, v)
				if v != 0 {
					same = false
				}
			}
			if !same {
				differing++
			}
		}
	}
	px := r.Dx() * r.Dy()
	d.Mean = float64(sum) / float64(3*px)
	d.Differing = float64(differing) / float64(px)
	return d, nil
}

func rgb8(img image.Image, x, y int) [3]int {
	r, g, b, _ := img.At(x, y).RGBA()
	return [3]int{int(r >> 8), int(g >> 8), int(b >> 8)}
}
