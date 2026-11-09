// Command compare checks this port against the live Rails service: it
// fetches avatars from it, renders the same ids locally, and reports SVGs
// that differ by a single byte and how far each PNG is from the live one.
//
//	go run ./cmd/compare -n 300 -png 30
//
// Live responses are cached on disk (under $TMPDIR by default), so a second
// run costs Heroku nothing, and requests are spaced out by -delay.
//
// With -record it instead writes the golden file the avatar tests read,
// from live, for the ids in -ids; -record-png saves one live PNG per icon
// for the raster tests.
package main

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/teamtito/starwavatars/internal/avatar"
	"github.com/teamtito/starwavatars/internal/raster"
)

func main() {
	base := flag.String("base", "https://starwavatars.herokuapp.com", "live service to compare against")
	n := flag.Int("n", 50, "random ids to compare as SVG")
	pngN := flag.Int("png", 0, "how many of those to compare as PNG too")
	seed := flag.Uint64("seed", 1, "seed for the random ids")
	idsFile := flag.String("ids", "", "file of ids, one per line, compared as well as (or, with -n 0, instead of) the random ones")
	cacheDir := flag.String("cache", filepath.Join(os.TempDir(), "starwavatars-compare"), "where live responses are cached")
	delay := flag.Duration("delay", 150*time.Millisecond, "pause between live requests")
	record := flag.String("record", "", "write golden JSON for the -ids to this file and exit")
	recordPNG := flag.String("record-png", "", "save one live PNG per icon from the -ids into this directory and exit")
	flag.BoolVar(&verbose, "v", false, "print every PNG's difference")
	flag.Parse()

	f := &fetcher{base: strings.TrimRight(*base, "/"), dir: *cacheDir, delay: *delay}
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		log.Fatal(err)
	}

	var ids []string
	if *idsFile != "" {
		var err error
		if ids, err = readIDs(*idsFile); err != nil {
			log.Fatal(err)
		}
	}
	switch {
	case *record != "":
		if err := writeGolden(f, ids, *record); err != nil {
			log.Fatal(err)
		}
		return
	case *recordPNG != "":
		if err := writePNGs(f, ids, *recordPNG); err != nil {
			log.Fatal(err)
		}
		return
	}

	r := rand.New(rand.NewPCG(*seed, *seed))
	for i := 0; i < *n; i++ {
		var b [16]byte
		for j := range b {
			b[j] = byte(r.UintN(256))
		}
		ids = append(ids, hex.EncodeToString(b[:]))
	}

	svgSame, svgDiff := 0, 0
	for _, id := range ids {
		live, err := f.get(id, "svg")
		if err != nil {
			log.Fatal(err)
		}
		local, err := avatar.New(id).SVG()
		if err != nil {
			log.Fatal(err)
		}
		if bytes.Equal(live, local) {
			svgSame++
		} else {
			svgDiff++
			fmt.Printf("SVG DIFFERS: %q (live %d bytes, local %d)\n", id, len(live), len(local))
		}
	}
	fmt.Printf("SVG: %d of %d byte-identical, %d differ\n", svgSame, len(ids), svgDiff)

	if *pngN > 0 {
		comparePNGs(f, ids[:min(*pngN, len(ids))])
	}
	if svgDiff > 0 {
		os.Exit(1)
	}
}

var verbose bool

type stats struct {
	n         int
	sum       float64 // mean absolute difference per channel, summed over images
	max       int
	differing float64 // fraction of pixels not identical, summed
	typeOK    int
}

func comparePNGs(f *fetcher, ids []string) {
	byIcon := map[string]*stats{}
	for _, id := range ids {
		live, err := f.get(id, "png")
		if err != nil {
			// Live cannot serve every id as a PNG: one with a control
			// character in it breaks the Content-Disposition header and
			// Heroku answers 503.
			fmt.Printf("PNG SKIPPED: %q: %v\n", id, err)
			continue
		}
		a := avatar.New(id)
		icon, _ := a.Icon()
		svg, err := a.SVG()
		if err != nil {
			log.Fatal(err)
		}
		local, err := raster.PNG(svg)
		if err != nil {
			log.Fatal(err)
		}
		d, err := raster.Diff(live, local)
		if err != nil {
			log.Fatalf("%s: %v", id, err)
		}
		if verbose {
			fmt.Printf("  %-34q %-13s mean %.4f max %3d differing %.2f%%\n", id, icon, d.Mean, d.Max, 100*d.Differing)
		}
		s := byIcon[icon]
		if s == nil {
			s = &stats{}
			byIcon[icon] = s
		}
		s.n++
		s.sum += d.Mean
		s.max = max(s.max, d.Max)
		s.differing += d.Differing
		if d.SameType {
			s.typeOK++
		}
	}
	icons := make([]string, 0, len(byIcon))
	for k := range byIcon {
		icons = append(icons, k)
	}
	sort.Strings(icons)
	compared := 0
	for _, s := range byIcon {
		compared += s.n
	}
	fmt.Printf("PNG: %d ids compared; mean |difference| per channel (0-255), worst channel, share of pixels not identical\n", compared)
	fmt.Printf("  %-13s %4s %8s %5s %9s %s\n", "icon", "ids", "mean", "max", "differing", "colour type")
	var total stats
	for _, k := range icons {
		s := byIcon[k]
		fmt.Printf("  %-13s %4d %8.4f %5d %8.2f%% %d/%d same\n", k, s.n, s.sum/float64(s.n), s.max, 100*s.differing/float64(s.n), s.typeOK, s.n)
		total.n += s.n
		total.sum += s.sum
		total.max = max(total.max, s.max)
		total.differing += s.differing
		total.typeOK += s.typeOK
	}
	fmt.Printf("  %-13s %4d %8.4f %5d %8.2f%% %d/%d same\n", "all", total.n, total.sum/float64(total.n), total.max, 100*total.differing/float64(total.n), total.typeOK, total.n)
}

// Golden is one recorded id, as live served it.
type Golden struct {
	ID        string `json:"id"`
	Icon      string `json:"icon"`
	RectFill  string `json:"rect_fill"`
	PathFill  string `json:"path_fill"`
	SVGLength int    `json:"svg_length"`
	SVGSHA256 string `json:"svg_sha256"`
}

var (
	artboard = regexp.MustCompile(`<g id="([^"]+)" sketch:type="MSArtboardGroup"`)
	rectTag  = regexp.MustCompile(`<rect [^>]*>`)
	pathTag  = regexp.MustCompile(`<(?:path|circle) [^>]*>`)
	fillAttr = regexp.MustCompile(` fill="#([0-9A-F]{6})"`)
)

func writeGolden(f *fetcher, ids []string, path string) error {
	var out []Golden
	for _, id := range ids {
		svg, err := f.get(id, "svg")
		if err != nil {
			return err
		}
		g := Golden{ID: id, SVGLength: len(svg)}
		sum := sha256.Sum256(svg)
		g.SVGSHA256 = hex.EncodeToString(sum[:])
		if m := artboard.FindSubmatch(svg); m != nil {
			g.Icon = string(m[1])
		}
		if m := fillAttr.FindSubmatch(rectTag.Find(svg)); m != nil {
			g.RectFill = string(m[1])
		}
		if m := fillAttr.FindSubmatch(pathTag.Find(svg)); m != nil {
			g.PathFill = string(m[1])
		}
		if g.Icon == "" || g.RectFill == "" || g.PathFill == "" {
			return fmt.Errorf("%q: could not read icon and colours from the live SVG", id)
		}
		out = append(out, g)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func writePNGs(f *fetcher, ids []string, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range ids {
		icon, _ := avatar.New(id).Icon()
		if seen[icon] {
			continue
		}
		seen[icon] = true
		b, err := f.get(id, "png")
		if err != nil {
			return err
		}
		// The file is named for the id, which the test re-renders.
		name := icon + "-" + strings.Map(func(r rune) rune {
			if strings.ContainsRune("0123456789abcdef", r) {
				return r
			}
			return '_'
		}, id) + ".png"
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func readIDs(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var ids []string
	sc := bufio.NewScanner(file)
	for sc.Scan() {
		// Lines are Go-quoted when they start with a quote, so ids with
		// control characters or spaces can be listed.
		line := sc.Text()
		if strings.HasPrefix(line, `"`) {
			var s string
			if err := json.Unmarshal([]byte(line), &s); err != nil {
				return nil, fmt.Errorf("%s: %w", line, err)
			}
			line = s
		}
		if line != "" {
			ids = append(ids, line)
		}
	}
	return ids, sc.Err()
}

// fetcher gets live avatars through a disk cache.
type fetcher struct {
	base  string
	dir   string
	delay time.Duration
	last  time.Time
}

func (f *fetcher) get(id, format string) ([]byte, error) {
	u := f.base + "/avatars/" + escapeSegment(id) + "." + format
	key := md5.Sum([]byte(u))
	cache := filepath.Join(f.dir, hex.EncodeToString(key[:]))
	if b, err := os.ReadFile(cache); err == nil {
		return b, nil
	}
	if wait := f.delay - time.Since(f.last); wait > 0 {
		time.Sleep(wait)
	}
	f.last = time.Now()
	resp, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	return b, os.WriteFile(cache, b, 0o644)
}

// escapeSegment percent-encodes everything but unreserved characters, so a
// dot or slash in an id stays part of it.
func escapeSegment(id string) string {
	var b strings.Builder
	for i := 0; i < len(id); i++ {
		c := id[i]
		if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') || c == '-' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
