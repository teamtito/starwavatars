package avatar

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

// golden is testdata/golden.json, recorded from the live Rails service by
// `go run ./cmd/compare -ids internal/avatar/testdata/ids.txt -record ...`.
type golden struct {
	ID        string `json:"id"`
	Icon      string `json:"icon"`
	RectFill  string `json:"rect_fill"`
	PathFill  string `json:"path_fill"`
	SVGLength int    `json:"svg_length"`
	SVGSHA256 string `json:"svg_sha256"`
}

func loadGolden(t *testing.T) []golden {
	t.Helper()
	b, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g []golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if len(g) < 50 {
		t.Fatalf("only %d golden ids", len(g))
	}
	return g
}

func TestMatchesLive(t *testing.T) {
	for _, g := range loadGolden(t) {
		a := New(g.ID)

		icon, err := a.Icon()
		if err != nil || icon != g.Icon {
			t.Errorf("%q: icon = %q, %v; live drew %q", g.ID, icon, err, g.Icon)
		}

		// The base colour's three components are unique_number(255, 2..4).
		for i := 0; i < 3; i++ {
			want, _ := strconv.ParseUint(g.RectFill[2*i:2*i+2], 16, 8)
			if got, err := a.UniqueNumber(255, 2+i); err != nil || got != int(want) {
				t.Errorf("%q: unique_number(255, %d) = %d, %v; live %d", g.ID, 2+i, got, err, want)
			}
		}
		base, err := a.Color()
		if err != nil || base.Hex != g.RectFill {
			t.Errorf("%q: color = %s, %v; live %s", g.ID, base.Hex, err, g.RectFill)
		}

		// deathstar.svg draws in #254962, which the model never replaced,
		// so its foreground colour never showed.
		fg, err := a.ForegroundColor()
		wantFG := g.PathFill
		if g.Icon == "deathstar" {
			if g.PathFill != "254962" {
				t.Errorf("%q: deathstar drawn in %s", g.ID, g.PathFill)
			}
		} else if err != nil || fg.Hex != wantFG {
			t.Errorf("%q: foreground = %s, %v; live %s", g.ID, fg.Hex, err, wantFG)
		}

		svg, err := a.SVG()
		if err != nil {
			t.Errorf("%q: %v", g.ID, err)
			continue
		}
		sum := sha256.Sum256(svg)
		if len(svg) != g.SVGLength || hex.EncodeToString(sum[:]) != g.SVGSHA256 {
			t.Errorf("%q: SVG is not byte-identical to live (%d bytes, live %d)", g.ID, len(svg), g.SVGLength)
		}
	}
}

func TestGoldenCoversEveryReachableIcon(t *testing.T) {
	seen := map[string]bool{}
	for _, g := range loadGolden(t) {
		seen[g.Icon] = true
	}
	for _, icon := range Icons[:14] {
		if !seen[icon] {
			t.Errorf("no golden id draws %s", icon)
		}
	}
}

func TestUniqueNumber(t *testing.T) {
	// md5("abc") = 900150983cd24fb0d6963f7d28e17f72, which as a Float is
	// 1.9141565834415877e+38; reversed and stripped to digits,
	// "8377851443856514191".
	a := New("abc")
	if a.digits != "8377851443856514191" {
		t.Fatalf("digits = %q", a.digits)
	}
	for _, tc := range []struct{ top, pos, want int }{
		{14, 1, 7},    // "5144": 14 * 0.5144 = 7.2016, falcon
		{255, 2, 36},  // "1443"
		{255, 3, 113}, // "4438"
		{255, 4, 111}, // "4385"
	} {
		if got, err := a.UniqueNumber(tc.top, tc.pos); err != nil || got != tc.want {
			t.Errorf("unique_number(%d, %d) = %d, %v; want %d", tc.top, tc.pos, got, err, tc.want)
		}
	}
}

func TestUniqueNumberPastTheEnd(t *testing.T) {
	// Ruby's "83012"[5, 4] is "" (0.0), but "83012"[6, 4] is nil, and
	// nil.to_f raised.
	a := &Avatar{digits: "83012"}
	if got, err := a.UniqueNumber(14, 1); err != nil || got != 0 {
		t.Errorf("at the end: %d, %v; want 0", got, err)
	}
	if _, err := a.UniqueNumber(255, 2); err != ErrNoDigits {
		t.Errorf("past the end: err = %v, want ErrNoDigits", err)
	}
	if _, err := a.SVG(); err != ErrNoDigits {
		t.Errorf("SVG: err = %v, want ErrNoDigits", err)
	}
}

func TestTieIsNeverChosen(t *testing.T) {
	// unique_number(14, 1) is 14 * (at most 0.9999), so at most 13.
	a := &Avatar{digits: "999999999999"}
	if icon, _ := a.Icon(); icon != "yoda" {
		t.Errorf("largest index draws %s, want yoda", icon)
	}
}

func TestRubyFloatToS(t *testing.T) {
	for _, tc := range []struct {
		v    float64
		want string
	}{
		{1.9141565834415877e+38, "1.9141565834415877e+38"},
		{1e20, "1.0e+20"},
		{1e38, "1.0e+38"},
		{123456789012345, "123456789012345.0"},
		{1234567890123456, "1.234567890123456e+15"},
		{100, "100.0"},
		{0, "0.0"},
		{1.5, "1.5"},
		{-2, "-2.0"},
		{12345.678, "12345.678"},
		{0.5, "0.5"},
		{0.0001, "0.0001"},
		{0.00001, "1.0e-05"},
		{1.25e-7, "1.25e-07"},
	} {
		if got := RubyFloatToS(tc.v); got != tc.want {
			t.Errorf("RubyFloatToS(%v) = %q, want %q", tc.v, got, tc.want)
		}
	}
}

func TestTemplatesAreUnixLineEnded(t *testing.T) {
	for _, name := range []string{"leia", "tie"} {
		src, err := IconSource(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "\r\n") {
			t.Errorf("%s.svg is no longer CRLF; the normalisation can go", name)
		}
		if strings.Contains(templates[name], "\r") {
			t.Errorf("%s template still has CR", name)
		}
	}
}
