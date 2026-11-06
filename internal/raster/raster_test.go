package raster

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teamtito/starwavatars/internal/avatar"
)

// testdata/live holds one PNG per icon (and one grey avatar) as the live
// Rails service drew it, named <icon>-<id>.png.
func TestPNGsAreCloseToLive(t *testing.T) {
	files, err := filepath.Glob("testdata/live/*.png")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, file := range files {
		icon, id, _ := strings.Cut(strings.TrimSuffix(filepath.Base(file), ".png"), "-")
		seen[icon] = true
		t.Run(icon+"-"+id[:6], func(t *testing.T) {
			live, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			a := avatar.New(id)
			if got, _ := a.Icon(); got != icon {
				t.Fatalf("id draws %s", got)
			}
			svg, err := a.SVG()
			if err != nil {
				t.Fatal(err)
			}
			local, err := PNG(svg)
			if err != nil {
				t.Fatal(err)
			}
			d, err := Diff(live, local)
			if err != nil {
				t.Fatal(err)
			}
			if !d.SameType {
				t.Errorf("colour type or bit depth differs: live %d/%d, local %d/%d", live[24], live[25], local[24], local[25])
			}
			// Measured: means of 0.005 to 0.035 and worst pixels of 3 to 11,
			// all on anti-aliased edges.
			if d.Mean > 0.05 || d.Max > 16 || d.Differing > 0.025 {
				t.Errorf("too far from live: mean %.4f, max %d, %.2f%% of pixels differ", d.Mean, d.Max, 100*d.Differing)
			}
		})
	}
	for _, icon := range avatar.Icons[:14] {
		if !seen[icon] {
			t.Errorf("no live PNG for %s", icon)
		}
	}
}

func TestEveryIconParses(t *testing.T) {
	for _, name := range avatar.Icons {
		src, err := avatar.IconSource(name)
		if err != nil {
			t.Fatal(err)
		}
		img, err := Rasterize(src)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if b := img.Bounds(); b.Dx() != 300 || b.Dy() != 300 {
			t.Errorf("%s: %v, want 300x300", name, b)
		}
	}
}

func TestColourTypes(t *testing.T) {
	for _, tc := range []struct {
		id        string
		colorType byte
	}{
		{"abc", 3},                              // palette
		{"863eec7f0cff5f2f24c30fea32344613", 0}, // grey base: greyscale
	} {
		svg, _ := avatar.New(tc.id).SVG()
		b, err := PNG(svg)
		if err != nil {
			t.Fatal(err)
		}
		if b[24] != 8 || b[25] != tc.colorType {
			t.Errorf("%s: bit depth %d colour type %d, want 8 and %d", tc.id, b[24], b[25], tc.colorType)
		}
		if _, err := png.Decode(bytes.NewReader(b)); err != nil {
			t.Error(err)
		}
	}
}

func TestManyColoursIsRGB(t *testing.T) {
	// 300 one-pixel columns in 300 colours.
	var b strings.Builder
	b.WriteString(`<svg viewBox="0 0 300 300" xmlns="http://www.w3.org/2000/svg">`)
	for i := 0; i < 300; i++ {
		fill := fmt.Sprintf("#%02X%02X00", i&0xff, i>>8)
		fmt.Fprintf(&b, `<rect x="%d" y="0" width="1" height="300" fill="%s"/>`, i, fill)
	}
	b.WriteString(`</svg>`)
	out, err := PNG([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if out[25] != 2 {
		t.Errorf("colour type %d, want 2 (RGB)", out[25])
	}
}

func TestRefusesWhatItCannotDraw(t *testing.T) {
	for _, src := range []string{
		`<svg viewBox="0 0 10 10"><path d="M0,0 L10,10" stroke="#000000"/></svg>`,
		`<svg viewBox="0 0 10 10"><ellipse cx="5" cy="5" rx="2" ry="3"/></svg>`,
		`<svg viewBox="0 0 10 10"><path d="M0,0 Q5,5 10,0 Z"/></svg>`,
		`<svg viewBox="0 0 10 10"><path d="M0,0 A5,5 0 0 1 10,0 Z"/></svg>`,
		`<svg viewBox="0 0 10 10"><rect width="5" height="5" fill="url(#g)"/></svg>`,
		`<svg viewBox="0 0 10 10"><g transform="scale(2)"><rect width="5" height="5"/></g></svg>`,
		`<svg viewBox="0 0 10 10"><rect width="5" height="5" style="fill:red"/></svg>`,
		`<svg width="10"><rect width="5" height="5"/></svg>`,
	} {
		if _, err := Rasterize([]byte(src)); !errors.Is(err, errUnsupported) {
			t.Errorf("%s: err = %v, want unsupported", src, err)
		}
	}
}

func TestPathNumbers(t *testing.T) {
	// Illustrator packs numbers: "-3.8-8.5" is two, ".5.5" is two.
	paths, err := parsePath("M10,10h5v5h-5z m1.5.5l1-1 1,1z")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("%d subpaths, want 2", len(paths))
	}
	want := []point{{10, 10}, {15, 10}, {15, 15}, {10, 15}}
	for i, p := range want {
		if paths[0][i] != p {
			t.Errorf("first subpath point %d = %v, want %v", i, paths[0][i], p)
		}
	}
	// The second starts relative to the first one's start, after its z.
	if got := paths[1][0]; got != (point{11.5, 10.5}) {
		t.Errorf("second subpath starts at %v, want {11.5 10.5}", got)
	}
	if got := paths[1][1]; got != (point{12.5, 9.5}) {
		t.Errorf("implicit lineto at %v, want {12.5 9.5}", got)
	}
}

func TestEvenOddLeavesHoles(t *testing.T) {
	// Two squares wound the same way: nonzero fills the middle, evenodd
	// leaves it.
	square := `M0,0 L10,0 L10,10 L0,10 Z M3,3 L7,3 L7,7 L3,7 Z`
	for rule, want := range map[string]uint8{"nonzero": 0, "evenodd": 255} {
		img, err := Rasterize([]byte(`<svg viewBox="0 0 10 10"><path fill-rule="` + rule + `" d="` + square + `"/></svg>`))
		if err != nil {
			t.Fatal(err)
		}
		if got := img.RGBAAt(5, 5).R; got != want {
			t.Errorf("%s: centre = %d, want %d", rule, got, want)
		}
		if got := img.RGBAAt(1, 1).R; got != 0 {
			t.Errorf("%s: ring = %d, want 0", rule, got)
		}
	}
}

func TestGroupOpacityIsAppliedOnce(t *testing.T) {
	// Black over white inside a group at 0.5, flattened onto white: 127
	// where they overlap, as cairo drew it, not a quarter-tone.
	img, err := Rasterize([]byte(`<svg viewBox="0 0 10 10"><g opacity="0.5">` +
		`<rect width="10" height="10" fill="#FFFFFF"/><rect width="5" height="10" fill="#000000"/></g></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.RGBAAt(2, 2).R; got != 127 {
		t.Errorf("overlap = %d, want 127", got)
	}
	if got := img.RGBAAt(7, 2).R; got != 255 {
		t.Errorf("white = %d, want 255", got)
	}
}
