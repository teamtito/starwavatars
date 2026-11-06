package raster

import (
	"testing"

	"github.com/teamtito/starwavatars/internal/avatar"
)

func BenchmarkPNG(b *testing.B) {
	svg, _ := avatar.New("abc").SVG()
	for b.Loop() {
		if _, err := PNG(svg); err != nil {
			b.Fatal(err)
		}
	}
}
