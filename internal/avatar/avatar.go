// Package avatar is the Starwavatar itself: which icon an id gets, which
// colours, and the SVG that results. It is a line-for-line port of the Rails
// app's StarwavatarAvatar model, quirks included, because every avatar ever
// handed out has to keep looking the same.
package avatar

import (
	"crypto/md5"
	"embed"
	"encoding/hex"
	"errors"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/teamtito/starwavatars/internal/paleta"
)

// Icons is the model's icon list, in its order. The index comes from
// UniqueNumber(14, 1), which is at most 13, so the last one, tie, is never
// chosen. (tie.svg is a copy of yoda's drawing anyway.)
var Icons = []string{
	"akbar",
	"atat",
	"boba",
	"c3po",
	"chewie",
	"darth",
	"deathstar",
	"falcon",
	"leia",
	"lightsaber",
	"r2d2",
	"stormtrooper",
	"xwing",
	"yoda",
	"tie",
}

//go:embed icons/*.svg
var iconFS embed.FS

// templates holds each icon as Rails served it. The model read the file and
// handed it to `render inline:`, which compiles it as an ERB template into
// Ruby source; Ruby's parser reads CRLF as LF, so the two icons saved with
// Windows line endings (leia, tie) went out with Unix ones. Nothing else in
// the files is ERB.
var templates = func() map[string]string {
	m := make(map[string]string, len(Icons))
	for _, name := range Icons {
		b, err := iconFS.ReadFile("icons/" + name + ".svg")
		if err != nil {
			panic(err)
		}
		m[name] = strings.ReplaceAll(string(b), "\r\n", "\n")
	}
	return m
}()

// IconSource returns the icon's file exactly as it is stored.
func IconSource(name string) ([]byte, error) { return iconFS.ReadFile("icons/" + name + ".svg") }

// ErrNoDigits is the model failing on an id whose number string is too short
// for the slice it takes: Ruby's String#[] returns nil past the end and
// nil.to_f raises, so Rails answered 500. No md5 digest is short enough for
// this to happen in practice.
var ErrNoDigits = errors.New("avatar: number string too short")

// Avatar is one id's avatar.
type Avatar struct {
	ID     string
	digits string
}

// New prepares the avatar for id, which is any string (usually an md5 hex
// digest of an email address).
func New(id string) *Avatar {
	sum := md5.Sum([]byte(id))
	n := new(big.Int).SetBytes(sum[:])
	f, _ := new(big.Float).SetInt(n).Float64() // Integer#to_f rounds to nearest, ties to even
	s := RubyFloatToS(f)
	// .reverse.gsub(/[^\d]/, '')
	var b strings.Builder
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] >= '0' && s[i] <= '9' {
			b.WriteByte(s[i])
		}
	}
	return &Avatar{ID: id, digits: b.String()}
}

// Digest is Digest::MD5.hexdigest(id).
func Digest(id string) string {
	sum := md5.Sum([]byte(id))
	return hex.EncodeToString(sum[:])
}

// UniqueNumber is the model's unique_number:
//
//	(top_number.to_f * ((Digest::MD5.hexdigest(md5).to_i(16)).to_f.to_s
//	  .reverse.gsub(/[^\d]/,'')[4 + position.to_i,4].to_f / 10000)).to_i
//
// The digest, read as a 128-bit integer, becomes a Float; its Ruby string
// form ("2.4057218839486356e+38") is reversed and stripped to digits, and
// four of those digits, read as a fraction, scale top.
func (a *Avatar) UniqueNumber(top, position int) (int, error) {
	start := 4 + position
	if start > len(a.digits) {
		return 0, ErrNoDigits
	}
	end := min(start+4, len(a.digits))
	frac := 0.0
	if start < end {
		v, err := strconv.ParseFloat(a.digits[start:end], 64)
		if err != nil {
			return 0, err
		}
		frac = v
	}
	return int(float64(top) * (frac / 10000)), nil
}

// Icon is the icon's name.
func (a *Avatar) Icon() (string, error) {
	i, err := a.UniqueNumber(14, 1)
	if err != nil {
		return "", err
	}
	return Icons[i], nil
}

// Color is the base colour, which fills the square behind the icon.
func (a *Avatar) Color() (paleta.Color, error) {
	var rgb [3]int
	for i := range rgb {
		v, err := a.UniqueNumber(255, 2+i)
		if err != nil {
			return paleta.Color{}, err
		}
		rgb[i] = v
	}
	return paleta.NewRGB(rgb[0], rgb[1], rgb[2])
}

// ForegroundColor is the icon's colour: the base colour darkened, unless it
// is already very dark, in which case lightened.
func (a *Avatar) ForegroundColor() (paleta.Color, error) {
	c, err := a.Color()
	if err != nil {
		return c, err
	}
	if c.Lightness > 20 {
		degree := 20.0
		if c.Lightness < 50 {
			degree = 40
		}
		return c.Darken(degree), nil
	}
	degree := 20.0
	if c.Lightness > 50 {
		degree = 40
	}
	return c.Lighten(degree), nil
}

// BackgroundColor is the model's background_color. The Rails app computed it
// but never drew it; it is here so the port of the model is complete.
//
// The model memoised both desaturated_color and complementary_color in the
// same @complimentary_color variable; only the desaturated one was ever
// asked for, so that slip changes nothing.
func (a *Avatar) BackgroundColor() (paleta.Color, error) {
	c, err := a.Color()
	if err != nil {
		return c, err
	}
	shades, err := paleta.GenerateShades(c, 5)
	if err != nil {
		return c, err
	}
	if shaded := shades[4]; c.Similarity(shaded) > 0.1 {
		return shaded, nil
	}
	if desaturated := c.Desaturate(); c.Similarity(desaturated) > 0.1 {
		return desaturated, nil
	}
	return c.Invert(), nil
}

// SVG is the avatar: the icon with its drawing colour (#264A62) swapped for
// the foreground and its white square swapped for the base colour, in that
// order, as two passes over the whole file.
func (a *Avatar) SVG() ([]byte, error) {
	icon, err := a.Icon()
	if err != nil {
		return nil, err
	}
	fg, err := a.ForegroundColor()
	if err != nil {
		return nil, err
	}
	base, err := a.Color()
	if err != nil {
		return nil, err
	}
	s := strings.ReplaceAll(templates[icon], "#264A62", "#"+fg.Hex)
	s = strings.ReplaceAll(s, "#FFFFFF", "#"+base.Hex)
	return []byte(s), nil
}

// RubyFloatToS is Ruby 2.3's Float#to_s (numeric.c, flo_to_s): the shortest
// digits that round-trip, written as "ddd.ddd" when there is a fractional
// part, "ddd.0" up to fifteen integer digits, "0.000ddd" down to 1e-4, and
// "d.ddde+XX" otherwise.
func RubyFloatToS(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "Infinity"
	case math.IsInf(v, -1):
		return "-Infinity"
	case math.IsNaN(v):
		return "NaN"
	}
	sign := ""
	if math.Signbit(v) {
		sign = "-"
	}
	e := strconv.FormatFloat(math.Abs(v), 'e', -1, 64) // "d.ddde+XX"
	mant, exp, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	x, _ := strconv.Atoi(exp)
	decpt := x + 1
	digs := len(digits)

	switch {
	case decpt > 0 && decpt < digs:
		return sign + digits[:decpt] + "." + digits[decpt:]
	case decpt > 0 && decpt <= 15: // DBL_DIG
		return sign + digits + strings.Repeat("0", decpt-digs) + ".0"
	case decpt <= 0 && decpt > -4:
		return sign + "0." + strings.Repeat("0", -decpt) + digits
	}
	frac := digits[1:]
	if frac == "" {
		frac = "0"
	}
	return sign + digits[:1] + "." + frac + "e" + signedExp(decpt-1)
}

// signedExp is C's "%+03d".
func signedExp(n int) string {
	s := strconv.Itoa(max(n, -n))
	if len(s) < 2 {
		s = "0" + s
	}
	if n < 0 {
		return "-" + s
	}
	return "+" + s
}
