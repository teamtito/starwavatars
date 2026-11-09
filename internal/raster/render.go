package raster

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"sort"
)

// subRows is how many sample rows each pixel row gets, as in cairo's
// scan converter (GRID_Y = 15). Across a row, coverage is exact.
const subRows = 15

// layer is an 8-bit premultiplied RGBA surface, like cairo's ARGB32.
type layer struct {
	w, h int
	pix  []uint8 // r, g, b, a
}

func newLayer(w, h int) *layer { return &layer{w: w, h: h, pix: make([]uint8, w*h*4)} }

// mul is pixman's MUL_UN8: a*b/255, rounded.
func mul(a, b uint8) uint8 {
	t := uint32(a)*uint32(b) + 0x80
	return uint8((t>>8 + t) >> 8)
}

// over composites a source pixel (premultiplied) onto dst.
func over(dst []uint8, sr, sg, sb, sa uint8) {
	inv := 255 - sa
	dst[0] = sr + mul(dst[0], inv)
	dst[1] = sg + mul(dst[1], inv)
	dst[2] = sb + mul(dst[2], inv)
	dst[3] = sa + mul(dst[3], inv)
}

// Rasterize draws the SVG and returns it flattened onto white.
func Rasterize(src []byte) (*image.RGBA, error) {
	doc, err := parse(src)
	if err != nil {
		return nil, err
	}
	l := newLayer(doc.width, doc.height)
	doc.draw(l, doc.root)

	// ImageMagick composited cairo's premultiplied result over its white
	// background: c + (255 - a) for each channel.
	img := image.NewRGBA(image.Rect(0, 0, doc.width, doc.height))
	for i := 0; i < len(l.pix); i += 4 {
		inv := 255 - l.pix[i+3]
		img.Pix[i+0] = l.pix[i+0] + inv
		img.Pix[i+1] = l.pix[i+1] + inv
		img.Pix[i+2] = l.pix[i+2] + inv
		img.Pix[i+3] = 255
	}
	return img, nil
}

func (doc *document) draw(dst *layer, n *node) {
	if n.hasShape {
		doc.fill(dst, n)
		return
	}
	if n.opacity >= 1 {
		for _, c := range n.children {
			doc.draw(dst, c)
		}
		return
	}
	// A group with opacity is drawn on its own surface and then painted
	// with that opacity, as librsvg did, so shapes inside it do not show
	// through each other.
	g := newLayer(dst.w, dst.h)
	for _, c := range n.children {
		doc.draw(g, c)
	}
	alpha := uint8(math.Floor(math.Max(0, math.Min(1, n.opacity))*255 + 0.5))
	for i := 0; i < len(g.pix); i += 4 {
		if g.pix[i+3] == 0 {
			continue
		}
		over(dst.pix[i:i+4], mul(g.pix[i], alpha), mul(g.pix[i+1], alpha), mul(g.pix[i+2], alpha), mul(g.pix[i+3], alpha))
	}
}

type crossing struct {
	x   float64
	dir int
}

// fill paints a shape's coverage in its colour.
func (doc *document) fill(dst *layer, n *node) {
	if len(n.polygon) == 0 {
		return
	}
	w, h := dst.w, dst.h
	rows := make([][]crossing, h*subRows)
	for _, sp := range n.polygon {
		for i := range sp {
			p, q := doc.device(sp[i]), doc.device(sp[(i+1)%len(sp)])
			dir := 1
			if p.y > q.y {
				p, q, dir = q, p, -1
			}
			if p.y == q.y {
				continue
			}
			// Sample rows at the centre of each sub-row: y = (k + 0.5) / subRows.
			k0 := int(math.Ceil(p.y*subRows - 0.5))
			k1 := int(math.Ceil(q.y*subRows - 0.5)) // exclusive
			k0 = max(k0, 0)
			k1 = min(k1, len(rows))
			slope := (q.x - p.x) / (q.y - p.y)
			for k := k0; k < k1; k++ {
				y := (float64(k) + 0.5) / subRows
				rows[k] = append(rows[k], crossing{p.x + (y-p.y)*slope, dir})
			}
		}
	}

	cov := make([]float64, w+1)
	for py := 0; py < h; py++ {
		clear(cov)
		touched := false
		for s := 0; s < subRows; s++ {
			xs := rows[py*subRows+s]
			if len(xs) == 0 {
				continue
			}
			touched = true
			sort.Slice(xs, func(i, j int) bool { return xs[i].x < xs[j].x })
			wind := 0
			for i := 0; i < len(xs)-1; i++ {
				wind += xs[i].dir
				inside := wind != 0
				if n.evenOdd {
					inside = wind%2 != 0
				}
				if inside {
					addSpan(cov, xs[i].x, xs[i+1].x, w)
				}
			}
		}
		if !touched {
			continue
		}
		row := dst.pix[py*w*4 : (py+1)*w*4]
		for px := 0; px < w; px++ {
			c := cov[px] / subRows
			if c <= 0 {
				continue
			}
			a := uint8(math.Floor(math.Min(c, 1)*255 + 0.5))
			if a == 0 {
				continue
			}
			over(row[px*4:px*4+4], mul(n.fill.r, a), mul(n.fill.g, a), mul(n.fill.b, a), a)
		}
	}
}

// addSpan adds the horizontal coverage of [x0, x1) to each pixel it crosses.
func addSpan(cov []float64, x0, x1 float64, w int) {
	x0 = math.Max(x0, 0)
	x1 = math.Min(x1, float64(w))
	if x1 <= x0 {
		return
	}
	i0, i1 := int(x0), int(x1)
	if i0 == i1 {
		cov[i0] += x1 - x0
		return
	}
	cov[i0] += float64(i0+1) - x0
	for i := i0 + 1; i < i1; i++ {
		cov[i]++
	}
	if i1 < w {
		cov[i1] += x1 - float64(i1)
	}
}

// device maps user space to pixels, rounding to cairo's 24.8 fixed point.
func (doc *document) device(p point) point {
	x := p.x*doc.scaleX + doc.offX
	y := p.y*doc.scaleY + doc.offY
	return point{math.Round(x*256) / 256, math.Round(y*256) / 256}
}

// PNG rasterizes the SVG and encodes it the way ImageMagick chose to: 8-bit
// greyscale when every pixel is grey, an 8-bit palette when there are at
// most 256 colours, and 8-bit RGB otherwise. Nothing is transparent.
//
// ImageMagick's greyscale was not the grey it drew. Writing a grey image it
// converted to its linear GRAY colourspace and stored those values as they
// were, so a grey avatar came out darker than the same drawing in colour
// (the 50% grey of a black square over white is 54, not 127). linearGrey
// does the same, so those avatars keep looking as they always have.
func PNG(src []byte) ([]byte, error) {
	img, err := Rasterize(src)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, reduce(img)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func reduce(img *image.RGBA) image.Image {
	grey := true
	index := map[[3]uint8]uint8{}
	var palette color.Palette
	for i := 0; i < len(img.Pix); i += 4 {
		r, g, b := img.Pix[i], img.Pix[i+1], img.Pix[i+2]
		if r != g || g != b {
			grey = false
		}
		k := [3]uint8{r, g, b}
		if _, ok := index[k]; !ok && len(palette) <= 256 {
			if len(palette) < 256 {
				index[k] = uint8(len(palette))
			}
			palette = append(palette, color.RGBA{r, g, b, 255})
		}
	}
	b := img.Bounds()
	if grey {
		out := image.NewGray(b)
		for i := range out.Pix {
			out.Pix[i] = linearGrey[img.Pix[i*4]]
		}
		return out
	}
	if len(palette) > 256 {
		return img
	}
	// Go writes a palette of 16 or fewer colours at a lower bit depth;
	// ImageMagick wrote 8 bits. Padding keeps the depth, and an avatar with
	// that few colours does not occur in practice.
	for len(palette) < 17 {
		palette = append(palette, palette[0])
	}
	out := image.NewPaletted(b, palette)
	for i := range out.Pix {
		p := img.Pix[i*4 : i*4+3]
		out.Pix[i] = index[[3]uint8{p[0], p[1], p[2]}]
	}
	return out
}

// linearGrey maps an 8-bit sRGB grey to the value ImageMagick 6 wrote for
// it: in 16-bit quantum, DecodePixelGamma, rounded to a quantum, then
// ScaleQuantumToChar.
var linearGrey = func() (t [256]uint8) {
	const q = 65535.0
	for v := range t {
		p := float64(v) * 257
		var lin float64
		if p <= 0.0404482362771076*q {
			lin = p / 12.92
		} else {
			lin = q * math.Pow((p/q+0.055)/1.055, 2.4)
		}
		quantum := uint32(math.Min(math.Max(math.Floor(lin+0.5), 0), q))
		t[v] = uint8((quantum + 128) / 257)
	}
	return t
}()
