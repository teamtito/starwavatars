// Package paleta is a port of the parts of the Paleta 0.3.0 Ruby gem
// (github.com/jordanstephens/paleta) that Starwavatars used, kept faithful
// to the gem's arithmetic so every colour comes out exactly as it did under
// Rails.
//
// Faithful means the gem's quirks too: HSL is rounded to eight decimal
// places with Ruby 2.3's Float#round, Float#% is Ruby's floored modulo, and
// RGB components are truncated (not rounded) back to integers. Lighten and
// Darken move only the lightness and never recompute hue or saturation from
// the new RGB.
//
// Only Color.Lightness, Lighten, Darken and Hex reach a Starwavatar's output
// (see avatar.ForegroundColor). The rest is ported because the Rails model
// called it, for its background colour, which it computed and never drew.
//
// Ruby rounded every floating-point operation separately. On arm64 the Go
// compiler fuses a multiply into a following add, which can round
// differently; the only products here that feed an addition are by 2 and
// 0.5, which are exact, so fused or not the result is the same bits.
package paleta

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
)

// ErrRange is the gem's "Component range exceeded" ArgumentError.
var ErrRange = errors.New("paleta: component range exceeded")

// Color is a Paleta::Color. RGB components are integers, as they are in the
// gem once set through its constructors or recomputed from HSL; HSL
// components are floats.
type Color struct {
	Red, Green, Blue           int
	Hue, Saturation, Lightness float64
	Hex                        string
}

// NewRGB is Paleta::Color.new(:rgb, red, green, blue).
func NewRGB(red, green, blue int) (Color, error) {
	var c Color
	for _, v := range []int{red, green, blue} {
		if v < 0 || v > 255 {
			return Color{}, ErrRange
		}
	}
	c.Red, c.Green, c.Blue = red, green, blue
	c.updateHSL()
	c.updateHex()
	return c, nil
}

// NewHSL is Paleta::Color.new(:hsl, hue, saturation, lightness).
func NewHSL(hue, saturation, lightness float64) (Color, error) {
	if !inRange(hue, 360) || !inRange(saturation, 100) || !inRange(lightness, 100) {
		return Color{}, ErrRange
	}
	c := Color{Hue: hue, Saturation: saturation, Lightness: lightness}
	c.updateRGB()
	c.updateHex()
	return c, nil
}

// inRange is Ruby's (0..max).include?(v), which is false for NaN.
func inRange(v, max float64) bool { return v >= 0 && v <= max }

// Lighten is Color#lighten: a copy with lightness raised by percentage,
// capped at 100.
func (c Color) Lighten(percentage float64) Color {
	c.Lightness += percentage
	if c.Lightness > 100 {
		c.Lightness = 100
	}
	c.updateRGB()
	c.updateHex()
	return c
}

// Darken is Color#darken: a copy with lightness lowered by percentage,
// floored at 0.
func (c Color) Darken(percentage float64) Color {
	c.Lightness -= percentage
	if c.Lightness < 0 {
		c.Lightness = 0
	}
	c.updateRGB()
	c.updateHex()
	return c
}

// Invert is Color#invert.
func (c Color) Invert() Color {
	c.Red, c.Green, c.Blue = 255-c.Red, 255-c.Green, 255-c.Blue
	c.updateHSL()
	c.updateHex()
	return c
}

// Desaturate is Color#desaturate.
func (c Color) Desaturate() Color {
	c.Saturation = 0
	c.updateRGB()
	c.updateHex()
	return c
}

// Complement is Color#complement.
func (c Color) Complement() Color {
	c.Hue = RubyMod(c.Hue+180, 360)
	c.updateRGB()
	c.updateHex()
	return c
}

// Similarity is Color#similarity: the RGB distance to o over the largest
// possible distance, 0 for identical and 1 for black against white.
func (c Color) Similarity(o Color) float64 {
	dr, dg, db := c.Red-o.Red, c.Green-o.Green, c.Blue-o.Blue
	sum := dr*dr + dg*dg + db*db
	return math.Sqrt(float64(sum)) / math.Sqrt(3*255*255)
}

func (c *Color) updateHSL() {
	r := float64(c.Red) / 255.0
	g := float64(c.Green) / 255.0
	b := float64(c.Blue) / 255.0

	min, max := r, r
	for _, v := range []float64{g, b} {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	delta := max - min

	h := 0.0
	l := (max + min) / 2.0
	var s float64
	switch {
	case l != 1 && l != 0 && delta/2.0 == l:
		s = 1.0
	case l == 0 || l == 1:
		s = 0
	default:
		s = delta / (1 - math.Abs(2*l-1))
	}

	if delta != 0 {
		// Ruby's `case max when r ... when g ... when b`: first match wins.
		switch max {
		case r:
			h = RubyMod((g-b)/delta, 6)
		case g:
			h = (b-r)/delta + 2
		case b:
			h = (r-g)/delta + 4
		}
	}

	c.Hue = RubyRound(h*60.0, 8)
	if c.Hue < 0 {
		c.Hue += 360
	}
	c.Saturation = RubyRound(s*100.0, 8)
	c.Lightness = RubyRound(l*100.0, 8)
}

func (c *Color) updateRGB() {
	h := c.Hue / 60.0
	s := c.Saturation / 100.0
	l := c.Lightness / 100.0

	d1 := (1 - math.Abs(2*l-1)) * s
	d2 := d1 * (1 - math.Abs(RubyMod(h, 2)-1))
	d3 := l - (d1 / 2.0)

	var r, g, b float64
	switch int(h) { // Ruby's h.to_i truncates toward zero, as int() does.
	case 0:
		r, g, b = d1, d2, 0
	case 1:
		r, g, b = d2, d1, 0
	case 2:
		r, g, b = 0, d1, d2
	case 3:
		r, g, b = 0, d2, d1
	case 4:
		r, g, b = d2, 0, d1
	case 5:
		r, g, b = d1, 0, d2
	}

	c.Red = int(255 * (r + d3))
	c.Green = int(255 * (g + d3))
	c.Blue = int(255 * (b + d3))
}

func (c *Color) updateHex() {
	c.Hex = strings.ToUpper(hexComponent(c.Red) + hexComponent(c.Green) + hexComponent(c.Blue))
}

// hexComponent is `n.to_s(16)`, zero-padded to two characters if shorter.
func hexComponent(n int) string {
	s := strconv.FormatInt(int64(n), 16)
	if len(s) < 2 {
		s = "0" + s
	}
	return s
}

// GenerateShades is Paleta::Palette.generate(:type => :shades,
// :from => :color, :color => color, :size => size): the seed colour plus
// size-1 copies stepping its lightness by 100/size, down until that would go
// below zero and then up from the seed, sorted by lightness.
func GenerateShades(color Color, size int) ([]Color, error) {
	palette := []Color{color}
	step := float64(100 / size)
	lightness := color.Lightness
	up := false
	for len(palette) != size {
		if lightness-step < 0 {
			up = true
			lightness = color.Lightness
		}
		if up {
			lightness += step
		} else {
			lightness -= step
		}
		shade, err := NewHSL(color.Hue, color.Saturation, lightness)
		if err != nil {
			return nil, err
		}
		palette = append(palette, shade)
	}
	sort.SliceStable(palette, func(i, j int) bool { return palette[i].Lightness < palette[j].Lightness })
	return palette, nil
}

// RubyMod is Ruby 2.3's Float#%: C's fmod, then shifted by the divisor when
// the signs differ, so the result takes the divisor's sign.
func RubyMod(x, y float64) float64 {
	if math.IsNaN(y) {
		return y
	}
	var mod float64
	if x == 0 || (math.IsInf(y, 0) && !math.IsInf(x, 0)) {
		mod = x
	} else {
		mod = math.Mod(x, y)
	}
	if y*mod < 0 {
		mod += y
	}
	return mod
}

// RubyRound is Ruby 2.3.6's Float#round(ndigits) for ndigits > 0
// (numeric.c, flo_round), including its correction step for products that
// land just below a half.
func RubyRound(number float64, ndigits int) float64 {
	const floatDig = 15 + 2 // DBL_DIG + 2
	if math.IsInf(number, 0) || math.IsNaN(number) {
		return number
	}
	_, binexp := math.Frexp(number)
	var hi, lo int
	if binexp > 0 {
		hi, lo = binexp/4, binexp/3+1
	} else {
		hi, lo = binexp/3-1, binexp/4
	}
	if ndigits >= floatDig-hi {
		return number
	}
	if ndigits < -lo {
		return 0
	}
	f := math.Pow(10, float64(ndigits))
	x := math.Round(number * f)
	if x > 0 {
		if (x+0.5)/f <= number {
			x++
		}
	} else {
		if (x-0.5)/f >= number {
			x--
		}
	}
	return x / f
}
