// Package raster turns a Starwavatar SVG into a PNG, in pure Go.
//
// It is not a general SVG renderer. It reads exactly the subset the fifteen
// icons use (groups with opacity, fill and fill-rule; rect, circle and path
// with a fill and an optional transform; the M L H V C S Z path commands
// and their relative forms) and refuses anything else, so a new icon that
// needs more fails loudly rather than drawing wrong.
//
// The Rails app rasterized with ImageMagick, which handed SVG to librsvg and
// cairo and then flattened the result onto white. The drawing here follows
// that pipeline closely enough to land within a few levels of it per pixel:
// cairo's curve flattening and 8-bit premultiplied compositing, a group's
// opacity applied to the group as a whole, and the white background put in
// at the end.
package raster

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// node is one drawable element after parsing: a group with children, or a
// shape with its outline already flattened in user space.
type node struct {
	opacity  float64 // groups only
	children []*node

	polygon  [][]point // shapes only: closed subpaths
	fill     rgb
	evenOdd  bool
	hasShape bool
}

type point struct{ x, y float64 }

type rgb struct{ r, g, b uint8 }

// style is what a child inherits from its group.
type style struct {
	fill     *rgb // nil is fill="none"
	evenOdd  bool
	stroking bool
}

type document struct {
	width, height int
	// viewBox → pixels
	scaleX, scaleY, offX, offY float64
	root                       *node
}

var errUnsupported = errors.New("raster: unsupported SVG")

func unsupported(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errUnsupported, fmt.Sprintf(format, args...))
}

// parse reads an icon. Elements that draw nothing (title, desc, defs,
// comments) are skipped; anything else outside the subset is an error.
func parse(src []byte) (*document, error) {
	dec := xml.NewDecoder(strings.NewReader(string(src)))
	dec.Strict = true
	var doc *document
	type frame struct {
		n     *node
		style style
		skip  int // depth inside an element that draws nothing
	}
	var stack []frame
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) > 0 && stack[len(stack)-1].skip > 0 {
				stack = append(stack, frame{skip: stack[len(stack)-1].skip + 1})
				continue
			}
			name := t.Name.Local
			if doc == nil {
				if name != "svg" {
					return nil, unsupported("root element %q", name)
				}
				d, err := parseRoot(t)
				if err != nil {
					return nil, err
				}
				doc = d
				doc.root = &node{opacity: 1}
				stack = append(stack, frame{n: doc.root, style: style{fill: &rgb{}}})
				continue
			}
			parent := stack[len(stack)-1]
			switch name {
			case "title", "desc", "defs":
				stack = append(stack, frame{skip: 1})
				continue
			}
			st, n, err := parseElement(t, parent.style)
			if err != nil {
				return nil, err
			}
			parent.n.children = append(parent.n.children, n)
			stack = append(stack, frame{n: n, style: st})
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, unsupported("unbalanced document")
			}
			stack = stack[:len(stack)-1]
		}
	}
	if doc == nil {
		return nil, unsupported("no svg element")
	}
	return doc, nil
}

func parseRoot(t xml.StartElement) (*document, error) {
	var vb []float64
	var w, h float64
	for _, a := range t.Attr {
		var err error
		switch {
		case a.Name.Local == "viewBox":
			vb, err = numbers(a.Value)
			if err == nil && (len(vb) != 4 || vb[2] <= 0 || vb[3] <= 0) {
				err = unsupported("viewBox %q", a.Value)
			}
		case a.Name.Local == "width":
			w, err = length(a.Value)
		case a.Name.Local == "height":
			h, err = length(a.Value)
		case a.Name.Local == "x" || a.Name.Local == "y":
			var v float64
			if v, err = length(a.Value); err == nil && v != 0 {
				err = unsupported("svg %s=%q", a.Name.Local, a.Value)
			}
		}
		if err != nil {
			return nil, err
		}
	}
	if vb == nil {
		return nil, unsupported("svg without a viewBox")
	}
	if w == 0 {
		w = vb[2]
	}
	if h == 0 {
		h = vb[3]
	}
	if w != vb[2]*h/vb[3] {
		return nil, unsupported("viewBox aspect differs from the size")
	}
	d := &document{width: int(math.Ceil(w)), height: int(math.Ceil(h))}
	d.scaleX, d.scaleY = w/vb[2], h/vb[3]
	d.offX, d.offY = -vb[0]*d.scaleX, -vb[1]*d.scaleY
	return d, nil
}

// parseElement reads a g, rect, circle or path.
func parseElement(t xml.StartElement, inherited style) (style, *node, error) {
	st := inherited
	n := &node{opacity: 1}
	m := identity
	attrs := map[string]string{}
	for _, a := range t.Attr {
		if a.Name.Space != "" && a.Name.Space != "http://www.w3.org/2000/svg" {
			continue // sketch:type, xml:space and friends
		}
		attrs[a.Name.Local] = a.Value
	}
	for k, v := range attrs {
		var err error
		switch k {
		case "id", "enable-background":
		case "fill":
			if v == "none" {
				st.fill = nil
			} else {
				var c rgb
				c, err = parseColor(v)
				st.fill = &c
			}
		case "fill-rule":
			switch v {
			case "evenodd":
				st.evenOdd = true
			case "nonzero":
				st.evenOdd = false
			default:
				err = unsupported("fill-rule %q", v)
			}
		case "stroke":
			st.stroking = v != "none"
		case "stroke-width":
		case "opacity":
			if t.Name.Local != "g" {
				err = unsupported("opacity on %s", t.Name.Local)
				break
			}
			n.opacity, err = strconv.ParseFloat(v, 64)
		case "transform":
			if t.Name.Local == "g" {
				err = unsupported("transform on g")
				break
			}
			m, err = parseTransform(v)
		case "d", "x", "y", "width", "height", "cx", "cy", "r":
		default:
			err = unsupported("attribute %s on %s", k, t.Name.Local)
		}
		if err != nil {
			return st, nil, err
		}
	}
	if st.stroking {
		return st, nil, unsupported("stroke on %s", t.Name.Local)
	}

	var subpaths [][]point
	var err error
	switch t.Name.Local {
	case "g":
		return st, n, nil
	case "rect":
		subpaths, err = rectPath(attrs)
	case "circle":
		subpaths, err = circlePath(attrs)
	case "path":
		subpaths, err = parsePath(attrs["d"])
	default:
		return st, nil, unsupported("element %s", t.Name.Local)
	}
	if err != nil {
		return st, nil, err
	}
	n.hasShape = true
	n.evenOdd = st.evenOdd
	if st.fill == nil {
		subpaths = nil
	} else {
		n.fill = *st.fill
	}
	for _, sp := range subpaths {
		for i := range sp {
			sp[i] = m.apply(sp[i])
		}
	}
	n.polygon = subpaths
	return st, n, nil
}

func attrNumber(attrs map[string]string, k string) (float64, error) {
	v, ok := attrs[k]
	if !ok {
		return 0, nil
	}
	return length(v)
}

func rectPath(attrs map[string]string) ([][]point, error) {
	var v [4]float64
	for i, k := range []string{"x", "y", "width", "height"} {
		f, err := attrNumber(attrs, k)
		if err != nil {
			return nil, err
		}
		v[i] = f
	}
	x, y, w, h := v[0], v[1], v[2], v[3]
	if w <= 0 || h <= 0 {
		return nil, nil
	}
	return [][]point{{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}}, nil
}

// circlePath draws the circle as four quarter arcs, each one cubic Bézier,
// as librsvg did.
func circlePath(attrs map[string]string) ([][]point, error) {
	var v [3]float64
	for i, k := range []string{"cx", "cy", "r"} {
		f, err := attrNumber(attrs, k)
		if err != nil {
			return nil, err
		}
		v[i] = f
	}
	cx, cy, r := v[0], v[1], v[2]
	if r <= 0 {
		return nil, nil
	}
	k := r * 4 / 3 * math.Tan(math.Pi/8)
	var f flattener
	f.moveTo(point{cx + r, cy})
	f.cubicTo(point{cx + r, cy + k}, point{cx + k, cy + r}, point{cx, cy + r})
	f.cubicTo(point{cx - k, cy + r}, point{cx - r, cy + k}, point{cx - r, cy})
	f.cubicTo(point{cx - r, cy - k}, point{cx - k, cy - r}, point{cx, cy - r})
	f.cubicTo(point{cx + k, cy - r}, point{cx + r, cy - k}, point{cx + r, cy})
	return f.done(), nil
}

func parseColor(v string) (rgb, error) {
	s, ok := strings.CutPrefix(v, "#")
	if !ok {
		return rgb{}, unsupported("colour %q", v)
	}
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return rgb{}, unsupported("colour %q", v)
	}
	n, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return rgb{}, unsupported("colour %q", v)
	}
	return rgb{uint8(n >> 16), uint8(n >> 8), uint8(n)}, nil
}

// length reads a number with an optional "px".
func length(v string) (float64, error) {
	v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "px"))
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, unsupported("length %q", v)
	}
	return f, nil
}

// numbers reads a whitespace- or comma-separated list.
func numbers(v string) ([]float64, error) {
	var out []float64
	s := scanner{s: v}
	for {
		s.skipSeparators()
		if s.eof() {
			return out, nil
		}
		f, err := s.number()
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
}

// matrix is an affine transform [a c e; b d f].
type matrix struct{ a, b, c, d, e, f float64 }

var identity = matrix{1, 0, 0, 1, 0, 0}

func (m matrix) mul(n matrix) matrix {
	return matrix{
		a: m.a*n.a + m.c*n.b,
		b: m.b*n.a + m.d*n.b,
		c: m.a*n.c + m.c*n.d,
		d: m.b*n.c + m.d*n.d,
		e: m.a*n.e + m.c*n.f + m.e,
		f: m.b*n.e + m.d*n.f + m.f,
	}
}

func (m matrix) apply(p point) point {
	return point{m.a*p.x + m.c*p.y + m.e, m.b*p.x + m.d*p.y + m.f}
}

// parseTransform reads a list of translate, rotate, scale and matrix.
func parseTransform(v string) (matrix, error) {
	m := identity
	rest := strings.TrimSpace(v)
	for rest != "" {
		name, args, ok := strings.Cut(rest, "(")
		if !ok {
			return m, unsupported("transform %q", v)
		}
		args, after, ok := strings.Cut(args, ")")
		if !ok {
			return m, unsupported("transform %q", v)
		}
		rest = strings.TrimLeft(after, " \t\r\n,")
		a, err := numbers(args)
		if err != nil {
			return m, err
		}
		var t matrix
		switch strings.TrimSpace(name) {
		case "translate":
			switch len(a) {
			case 1:
				t = matrix{1, 0, 0, 1, a[0], 0}
			case 2:
				t = matrix{1, 0, 0, 1, a[0], a[1]}
			default:
				return m, unsupported("transform %q", v)
			}
		case "scale":
			switch len(a) {
			case 1:
				t = matrix{a[0], 0, 0, a[0], 0, 0}
			case 2:
				t = matrix{a[0], 0, 0, a[1], 0, 0}
			default:
				return m, unsupported("transform %q", v)
			}
		case "rotate":
			if len(a) != 1 && len(a) != 3 {
				return m, unsupported("transform %q", v)
			}
			s, c := math.Sincos(a[0] * math.Pi / 180)
			t = matrix{c, s, -s, c, 0, 0}
			if len(a) == 3 {
				t = matrix{1, 0, 0, 1, a[1], a[2]}.mul(t).mul(matrix{1, 0, 0, 1, -a[1], -a[2]})
			}
		case "matrix":
			if len(a) != 6 {
				return m, unsupported("transform %q", v)
			}
			t = matrix{a[0], a[1], a[2], a[3], a[4], a[5]}
		default:
			return m, unsupported("transform %q", v)
		}
		m = m.mul(t)
	}
	return m, nil
}

// parsePath reads path data into flattened, closed subpaths.
func parsePath(d string) ([][]point, error) {
	var f flattener
	s := scanner{s: d}
	var cmd byte
	var cur, start, lastCtrl point
	prevCubic := false
	for {
		s.skipSeparators()
		if s.eof() {
			break
		}
		if c := s.peek(); isCommand(c) {
			cmd = c
			s.i++
		} else if cmd == 0 {
			return nil, unsupported("path data %q", d)
		}
		rel := cmd >= 'a'
		base := point{}
		if rel {
			base = cur
		}
		var nums [6]float64
		need := argCount[upper(cmd)]
		if upper(cmd) == 'Z' {
			cur = start
			f.close()
			prevCubic = false
			// A Z takes no arguments; a number after it is an implicit
			// command only after another letter.
			cmd = 0
			continue
		}
		for i := 0; i < need; i++ {
			s.skipSeparators()
			v, err := s.number()
			if err != nil {
				return nil, unsupported("path data %q: %v", d, err)
			}
			nums[i] = v
		}
		isCubic := false
		switch upper(cmd) {
		case 'M':
			cur = point{base.x + nums[0], base.y + nums[1]}
			start = cur
			f.moveTo(cur)
			// Further pairs after a moveto are linetos.
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
		case 'L':
			cur = point{base.x + nums[0], base.y + nums[1]}
			f.lineTo(cur)
		case 'H':
			cur = point{base.x + nums[0], cur.y}
			f.lineTo(cur)
		case 'V':
			cur = point{cur.x, base.y + nums[0]}
			f.lineTo(cur)
		case 'C':
			c1 := point{base.x + nums[0], base.y + nums[1]}
			c2 := point{base.x + nums[2], base.y + nums[3]}
			end := point{base.x + nums[4], base.y + nums[5]}
			f.cubicTo(c1, c2, end)
			lastCtrl, cur, isCubic = c2, end, true
		case 'S':
			c1 := cur
			if prevCubic {
				c1 = point{2*cur.x - lastCtrl.x, 2*cur.y - lastCtrl.y}
			}
			c2 := point{base.x + nums[0], base.y + nums[1]}
			end := point{base.x + nums[2], base.y + nums[3]}
			f.cubicTo(c1, c2, end)
			lastCtrl, cur, isCubic = c2, end, true
		}
		prevCubic = isCubic
	}
	return f.done(), nil
}

var argCount = map[byte]int{'M': 2, 'L': 2, 'H': 1, 'V': 1, 'C': 6, 'S': 4, 'Z': 0}

func isCommand(c byte) bool { return strings.IndexByte("MmLlHhVvCcSsZz", c) >= 0 }

func upper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}

type scanner struct {
	s string
	i int
}

func (s *scanner) eof() bool  { return s.i >= len(s.s) }
func (s *scanner) peek() byte { return s.s[s.i] }

func (s *scanner) skipSeparators() {
	for !s.eof() && strings.IndexByte(" \t\r\n,", s.s[s.i]) >= 0 {
		s.i++
	}
}

// number reads one SVG number: "-3.8-8.5" is two numbers and ".5.5" is too.
func (s *scanner) number() (float64, error) {
	start := s.i
	if !s.eof() && (s.peek() == '-' || s.peek() == '+') {
		s.i++
	}
	digits := 0
	for !s.eof() && s.peek() >= '0' && s.peek() <= '9' {
		s.i++
		digits++
	}
	if !s.eof() && s.peek() == '.' {
		s.i++
		for !s.eof() && s.peek() >= '0' && s.peek() <= '9' {
			s.i++
			digits++
		}
	}
	if digits == 0 {
		return 0, fmt.Errorf("number expected at %d", start)
	}
	if !s.eof() && (s.peek() == 'e' || s.peek() == 'E') {
		j := s.i + 1
		if j < len(s.s) && (s.s[j] == '-' || s.s[j] == '+') {
			j++
		}
		if j < len(s.s) && s.s[j] >= '0' && s.s[j] <= '9' {
			s.i = j
			for !s.eof() && s.peek() >= '0' && s.peek() <= '9' {
				s.i++
			}
		}
	}
	return strconv.ParseFloat(s.s[start:s.i], 64)
}

// flattener turns path segments into polygons, splitting curves the way
// cairo's _cairo_spline_decompose does at its default tolerance of 0.1.
type flattener struct {
	paths [][]point
	cur   []point
}

const tolerance = 0.1

func (f *flattener) moveTo(p point) {
	f.flush()
	f.cur = []point{p}
}

func (f *flattener) lineTo(p point) {
	if len(f.cur) == 0 {
		f.cur = []point{p}
		return
	}
	f.cur = append(f.cur, p)
}

func (f *flattener) close() {
	if len(f.cur) > 0 {
		start := f.cur[0]
		f.flush()
		f.cur = []point{start}
	}
}

func (f *flattener) flush() {
	if len(f.cur) > 2 {
		f.paths = append(f.paths, f.cur)
	}
	f.cur = nil
}

func (f *flattener) done() [][]point {
	f.flush()
	return f.paths
}

func (f *flattener) cubicTo(b, c, d point) {
	if len(f.cur) == 0 {
		f.cur = []point{b}
	}
	a := f.cur[len(f.cur)-1]
	f.cur = f.cur[:len(f.cur)-1] // decompose puts a back as its first point
	f.decompose(a, b, c, d)
	f.cur = append(f.cur, d)
}

func (f *flattener) decompose(a, b, c, d point) {
	if splineError(a, b, c, d) < tolerance*tolerance {
		f.cur = append(f.cur, a)
		return
	}
	ab, bc, cd := mid(a, b), mid(b, c), mid(c, d)
	abbc, bccd := mid(ab, bc), mid(bc, cd)
	m := mid(abbc, bccd)
	f.decompose(a, ab, abbc, m)
	f.decompose(m, bccd, cd, d)
}

func mid(p, q point) point { return point{(p.x + q.x) / 2, (p.y + q.y) / 2} }

// splineError is cairo's _cairo_spline_error_squared: the larger squared
// distance of the two control points from the chord.
func splineError(a, b, c, d point) float64 {
	bdx, bdy := b.x-a.x, b.y-a.y
	cdx, cdy := c.x-a.x, c.y-a.y
	if a != d {
		dx, dy := d.x-a.x, d.y-a.y
		v := dx*dx + dy*dy
		if u := bdx*dx + bdy*dy; u >= v {
			bdx -= dx
			bdy -= dy
		} else if u > 0 {
			bdx -= u / v * dx
			bdy -= u / v * dy
		}
		if u := cdx*dx + cdy*dy; u >= v {
			cdx -= dx
			cdy -= dy
		} else if u > 0 {
			cdx -= u / v * dx
			cdy -= u / v * dy
		}
	}
	return math.Max(bdx*bdx+bdy*bdy, cdx*cdx+cdy*cdy)
}
