package paleta

import (
	"math"
	"testing"
)

// These cases are the gem's own spec (spec/models/color_spec.rb and
// palette_spec.rb in paleta 0.3.0), for the methods ported here.

func mustRGB(t *testing.T, r, g, b int) Color {
	t.Helper()
	c, err := NewRGB(r, g, b)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustHSL(t *testing.T, h, s, l float64) Color {
	t.Helper()
	c, err := NewHSL(h, s, l)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewRGBComputesHSLAndHex(t *testing.T) {
	c := mustRGB(t, 237, 172, 33)
	if int(c.Hue) != 40 || int(c.Saturation) != 85 || int(c.Lightness) != 52 {
		t.Errorf("hsl = %v %v %v, want 40 85 52 truncated", c.Hue, c.Saturation, c.Lightness)
	}
	if got := mustRGB(t, 94, 161, 235).Hex; got != "5EA1EB" {
		t.Errorf("hex = %q, want 5EA1EB", got)
	}
}

func TestNewHSLComputesRGB(t *testing.T) {
	c := mustHSL(t, 0, 0, 100)
	if c.Red != 255 || c.Green != 255 || c.Blue != 255 || c.Hex != "FFFFFF" {
		t.Errorf("hsl(0,0,100) = %+v, want white", c)
	}
	// "should update its RGB value when its HSL value is updated"
	for _, tc := range []struct {
		h, s, l    float64
		r, g, b    int
		annotation string
	}{
		{120, 100, 50, 0, 255, 0, "hue 120"},
		{120, 50, 50, 63, 191, 63, "saturation 50"},
		{120, 50, 80, 178, 229, 178, "lightness 80"},
	} {
		c := mustHSL(t, tc.h, tc.s, tc.l)
		if c.Red != tc.r || c.Green != tc.g || c.Blue != tc.b {
			t.Errorf("%s: rgb = %d %d %d, want %d %d %d", tc.annotation, c.Red, c.Green, c.Blue, tc.r, tc.g, tc.b)
		}
	}
}

func TestRangeErrors(t *testing.T) {
	if _, err := NewRGB(-74, 333, 4321); err != ErrRange {
		t.Errorf("rgb out of range: err = %v", err)
	}
	for _, hsl := range [][3]float64{{400, 50, 50}, {200, 150, 50}, {200, 50, 150}, {math.NaN(), 0, 0}} {
		if _, err := NewHSL(hsl[0], hsl[1], hsl[2]); err != ErrRange {
			t.Errorf("hsl %v: err = %v", hsl, err)
		}
	}
}

func TestLightenAndDarken(t *testing.T) {
	c := mustRGB(t, 94, 161, 235)
	if got := c.Lighten(20).Lightness; got != c.Lightness+20 {
		t.Errorf("lighten(20) = %v, want %v", got, c.Lightness+20)
	}
	if got := c.Darken(20).Lightness; got != c.Lightness-20 {
		t.Errorf("darken(20) = %v, want %v", got, c.Lightness-20)
	}
	if got := c.Lighten(300).Lightness; got != 100 {
		t.Errorf("lighten(300) = %v, want 100", got)
	}
	if got := c.Darken(300).Lightness; got != 0 {
		t.Errorf("darken(300) = %v, want 0", got)
	}
	if c.Lighten(20); c.Lightness != mustRGB(t, 94, 161, 235).Lightness {
		t.Error("lighten changed the receiver")
	}
}

func TestInvertDesaturateComplement(t *testing.T) {
	c := mustRGB(t, 94, 161, 235)
	if inv := c.Invert(); inv.Red != 161 || inv.Green != 94 || inv.Blue != 20 {
		t.Errorf("invert = %d %d %d, want 161 94 20", inv.Red, inv.Green, inv.Blue)
	}
	d := c.Desaturate()
	if d.Saturation != 0 || d.Red != 164 || d.Green != 164 || d.Blue != 164 {
		t.Errorf("desaturate = %+v, want 164 grey", d)
	}
	if int(c.Saturation) != 77 {
		t.Errorf("receiver saturation = %v, want 77", c.Saturation)
	}
	if got := mustHSL(t, 90, 50, 50).Complement().Hue; int(got) != 270 {
		t.Errorf("complement hue = %v, want 270", got)
	}
}

func TestSimilarity(t *testing.T) {
	a, b := mustRGB(t, 94, 161, 235), mustRGB(t, 237, 172, 33)
	if got := RubyRound(a.Similarity(b), 5); got != 0.56091 {
		t.Errorf("similarity = %v, want 0.56091", got)
	}
	if got := b.Similarity(b); got != 0 {
		t.Errorf("self similarity = %v", got)
	}
	if got := mustRGB(t, 0, 0, 0).Similarity(mustRGB(t, 255, 255, 255)); got != 1 {
		t.Errorf("black/white similarity = %v", got)
	}
}

func TestGenerateShades(t *testing.T) {
	red := mustRGB(t, 255, 0, 0)
	p, err := GenerateShades(red, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{10, 30, 50, 70, 90}
	for i, c := range p {
		if c.Hue != red.Hue || c.Saturation != red.Saturation {
			t.Errorf("shade %d hs = %v %v", i, c.Hue, c.Saturation)
		}
		if c.Lightness != want[i] {
			t.Errorf("shade %d lightness = %v, want %v", i, c.Lightness, want[i])
		}
	}
}

func TestRubyMod(t *testing.T) {
	for _, tc := range []struct{ x, y, want float64 }{
		{-0.5, 6, 5.5}, {7, 6, 1}, {0, 6, 0}, {370, 360, 10}, {-1, 2, 1}, {3.5, 2, 1.5},
	} {
		if got := RubyMod(tc.x, tc.y); got != tc.want {
			t.Errorf("RubyMod(%v, %v) = %v, want %v", tc.x, tc.y, got, tc.want)
		}
	}
}

func TestRubyRound(t *testing.T) {
	if math.Pow(10, 8) != 1e8 {
		t.Fatal("math.Pow(10, 8) is not exact")
	}
	for _, tc := range []struct{ x, want float64 }{
		{0, 0},
		{1.0 / 3 * 100, 33.33333333},
		{359.999999999, 360},
		{12.345678905, 12.34567891},
		{2.5e-9, 0}, // below half of the eighth place rounds to zero
	} {
		if got := RubyRound(tc.x, 8); got != tc.want {
			t.Errorf("RubyRound(%v, 8) = %v, want %v", tc.x, got, tc.want)
		}
	}
}
