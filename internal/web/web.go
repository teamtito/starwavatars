// Package web is the HTTP side of Starwavatars. It answers the way the Rails
// app did, down to the quirks of Rails 4.2's router, so every URL already
// out in the world (Gravatar fallbacks, cached CDN links) keeps resolving to
// the same thing:
//
//   - GET /avatars/:id(.:format), format svg by default. The id and the
//     format are each one path segment with no dot in it, so "a.b.png" and
//     "abc." match nothing and are 404s. A dot escaped as %2E is part of the
//     id.
//   - Before matching, slashes are squeezed and trailing ones dropped
//     ("//avatars//abc/" is "/avatars/abc"). The segments are then
//     percent-decoded, without turning "+" into a space.
//   - An id or format that is not valid UTF-8 once decoded is a 400.
//   - A format other than svg or png is a 406, with the error rendered in
//     that format when Rails could (json, xml, yaml) and an empty body
//     otherwise. Format names are case-sensitive: ".PNG" is a 406.
//   - /robots.txt and /favicon.ico are the files Rails had in public/.
//   - Anything else, including any method but GET and HEAD, is the Rails
//     404 page (or its json, xml or yaml form, chosen by a ?format= query
//     parameter or the Accept header, as Rails chose).
package web

import (
	"bytes"
	"crypto/md5"
	_ "embed"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/teamtito/starwavatars/internal/avatar"
	"github.com/teamtito/starwavatars/internal/raster"
)

// CacheControl goes on every avatar. An avatar is a pure function of its
// URL, so it can be cached forever.
const CacheControl = "public, max-age=31536000, immutable"

//go:embed pages/404.html
var notFoundPage []byte

//go:embed pages/500.html
var errorPage []byte

// Rails also served what was in public/. Two of those files are still worth
// answering for: robots.txt (comments only, so crawlers may go anywhere) and
// an empty favicon.ico. The error pages it exposed there, and its empty
// asset bundle, are not served.
var (
	//go:embed pages/robots.txt
	robotsTxt []byte
	//go:embed pages/favicon.ico
	favicon []byte
)

// Handler serves Starwavatars. build is what /up reports.
func Handler(build string) http.Handler {
	return newHandler(build, max(2, runtime.NumCPU()), renderWait)
}

func newHandler(build string, slots int, wait time.Duration) *handler {
	return &handler{build: build, renders: make(chan struct{}, slots), wait: wait}
}

type handler struct {
	build string
	// renders is one slot per PNG being drawn. Drawing is CPU and memory,
	// and every distinct id is a fresh drawing, so a burst of them waits
	// for a slot rather than piling up (Astra review, 2026-11-06).
	renders chan struct{}
	wait    time.Duration
}

// renderWait is how long a PNG request waits for a slot before it is told
// to come back later. A CDN in front retries; a person sees a broken image
// for one load at worst.
const renderWait = 10 * time.Second

// maxID is the longest id served. Real ones are md5s, 32 characters; the
// cap stops an id near the server's header limit costing a drawing and a
// filename that size.
const maxID = 1024

var avatarRoute = regexp.MustCompile(`^/avatars/([^/.?]+)(?:\.([^/.?]+))?$`)

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := normalizePath(rawPath(r))
	if path == "/up" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, "ok %s\n", h.build)
		return
	}

	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		switch path {
		case "/robots.txt":
			staticFile(w, "text/plain", robotsTxt)
			return
		case "/favicon.ico":
			staticFile(w, "image/vnd.microsoft.icon", favicon)
			return
		}
	}

	m := avatarRoute.FindStringSubmatch(path)
	if m == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		h.notFound(w, r)
		return
	}
	id, format := unescape(m[1]), "svg"
	if m[2] != "" {
		format = unescape(m[2])
	}
	if !utf8.ValidString(id) || !utf8.ValidString(format) {
		emptyError(w, http.StatusBadRequest)
		return
	}
	if len(id) > maxID {
		emptyError(w, http.StatusRequestURITooLong)
		return
	}

	switch format {
	case "svg":
		body, err := avatar.New(id).SVG()
		if err != nil {
			h.serverError(w, r, id, err)
			return
		}
		serveAvatar(w, r, "image/svg+xml; charset=utf-8", body)
	case "png":
		if !h.acquire(w, r) {
			return
		}
		defer func() { <-h.renders }()
		svg, err := avatar.New(id).SVG()
		var body []byte
		if err == nil {
			body, err = raster.PNG(svg)
		}
		if err != nil {
			h.serverError(w, r, id, err)
			return
		}
		w.Header().Set("Content-Disposition", `inline; filename="`+headerSafe(id)+`.png"`)
		w.Header().Set("Content-Transfer-Encoding", "binary")
		serveAvatar(w, r, "image/png", body)
	default:
		errorIn(w, http.StatusNotAcceptable, mimeByExtension[format])
	}
}

// acquire takes a drawing slot, or answers for itself and says false: 503
// with Retry-After when none came free in time, nothing when the client
// gave up first.
func (h *handler) acquire(w http.ResponseWriter, r *http.Request) bool {
	t := time.NewTimer(h.wait)
	defer t.Stop()
	select {
	case h.renders <- struct{}{}:
		return true
	case <-r.Context().Done():
		return false
	case <-t.C:
		w.Header().Set("Retry-After", "5")
		emptyError(w, http.StatusServiceUnavailable)
		return false
	}
}

func staticFile(w http.ResponseWriter, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

func serveAvatar(w http.ResponseWriter, r *http.Request, contentType string, body []byte) {
	sum := md5.Sum(body)
	etag := `W/"` + hex.EncodeToString(sum[:]) + `"`
	hdr := w.Header()
	hdr.Set("Cache-Control", CacheControl)
	hdr.Set("ETag", etag)
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("X-Frame-Options", "SAMEORIGIN")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	hdr.Set("Content-Type", contentType)
	hdr.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	w.Write(body)
}

// etagMatches is Rack::ConditionalGet's test: the header, split on commas,
// contains the tag or "*".
func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, t := range strings.Split(header, ",") {
		if t = strings.TrimSpace(t); t == etag || t == "*" {
			return true
		}
	}
	return false
}

// headerSafe stops an id breaking the Content-Disposition header. Rails
// wrote the id into the header as it was: a control character made
// Heroku's router answer 503 for the whole response, and a quote let the
// id close the filename and add parameters of its own (filename*=, Astra
// review, 2026-11-06). Here control characters, quotes and backslashes
// become underscores and the avatar is served. Tab was legal and stays.
func headerSafe(id string) string {
	return strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\t') || r == 0x7f || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, id)
}

func (h *handler) serverError(w http.ResponseWriter, r *http.Request, id string, err error) {
	log.Printf("avatar %q: %v", id, err)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(errorPage)))
	w.WriteHeader(http.StatusInternalServerError)
	if r.Method != http.MethodHead {
		w.Write(errorPage)
	}
}

func (h *handler) notFound(w http.ResponseWriter, r *http.Request) {
	if format := requestFormat(r); format != "" {
		errorIn(w, http.StatusNotFound, format)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(notFoundPage)))
	w.WriteHeader(http.StatusNotFound)
	if r.Method != http.MethodHead {
		w.Write(notFoundPage)
	}
}

// errorIn renders an error the way ActionDispatch::PublicExceptions did:
// as {status, error} in json, xml or yaml when the request's format was one
// of those (Hash#to_json, #to_xml, #to_yaml), and otherwise as an empty
// text/html body, because the app had no public page for the status.
func errorIn(w http.ResponseWriter, status int, format string) {
	code, text := strconv.Itoa(status), http.StatusText(status)
	var body, contentType string
	switch format {
	case "json":
		contentType = "application/json"
		body = `{"status":"` + code + `","error":"` + text + `"}`
	case "xml":
		contentType = "application/xml"
		body = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<hash>\n  <status>" + code +
			"</status>\n  <error>" + text + "</error>\n</hash>\n"
	case "yaml":
		contentType = "application/x-yaml"
		body = "---\n:status: '" + code + "'\n:error: " + text + "\n"
	default:
		emptyError(w, status)
		return
	}
	w.Header().Set("Content-Type", contentType+"; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	w.Write([]byte(body))
}

func emptyError(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(status)
}

// The only formats whose name matters here are the three Rails could render
// an error in. Every other extension, registered with Rails or not, ends the
// same way: an empty body.
var mimeByExtension = map[string]string{"json": "json", "xml": "xml", "yaml": "yaml"}

var mimeByType = map[string]string{
	"application/json":        "json",
	"text/x-json":             "json",
	"application/jsonrequest": "json",
	"application/xml":         "xml",
	"text/xml":                "xml",
	"application/x-xml":       "xml",
	"application/x-yaml":      "yaml",
	"text/yaml":               "yaml",
}

var browserLikeAccepts = regexp.MustCompile(`,\s*\*/\*|\*/\*\s*,`)

// requestFormat is how Rails picked the format of a request that matched no
// route: a ?format= parameter, else a non-browser Accept header, else HTML
// (returned as ""). Only json, xml and yaml are reported, since only they
// change the response. Rails' special ordering for application/xml in a
// list of equal-quality types is not reproduced.
func requestFormat(r *http.Request) string {
	if q, err := url.ParseQuery(r.URL.RawQuery); err == nil {
		if v, ok := q["format"]; ok && len(v) > 0 {
			return mimeByExtension[v[len(v)-1]]
		}
	}
	accept := r.Header.Get("Accept")
	if accept == "" || browserLikeAccepts.MatchString(accept) {
		return ""
	}
	if !strings.Contains(accept, ",") {
		return mimeByType[strings.TrimSpace(strings.Split(accept, ";")[0])]
	}
	type item struct {
		name string
		q    float64
	}
	var items []item
	for _, part := range strings.Split(accept, ",") {
		name, params, _ := strings.Cut(part, ";")
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		items = append(items, item{name, q})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].q > items[j].q })
	if len(items) == 0 {
		return ""
	}
	return mimeByType[items[0].name]
}

// rawPath is the request path still percent-encoded, as Rack's PATH_INFO
// was, so that %2F stays inside a segment.
func rawPath(r *http.Request) string {
	if p := r.RequestURI; strings.HasPrefix(p, "/") {
		p, _, _ = strings.Cut(p, "?")
		return p
	}
	return r.URL.EscapedPath()
}

// normalizePath is Journey::Router::Utils.normalize_path: squeeze runs of
// slashes and drop trailing ones.
func normalizePath(p string) string {
	b := make([]byte, 1, len(p)+1)
	b[0] = '/'
	for i := 0; i < len(p); i++ {
		if p[i] == '/' && b[len(b)-1] == '/' {
			continue
		}
		b = append(b, p[i])
	}
	s := strings.TrimRight(string(b), "/")
	if s == "" {
		return "/"
	}
	return s
}

// unescape is Journey's unescape_uri: every %XX becomes its byte, "+" stays.
// Go's server has already refused a request line with a malformed escape.
func unescape(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b bytes.Buffer
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			v, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			b.WriteByte(byte(v))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isHex(c byte) bool {
	return ('0' <= c && c <= '9') || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F')
}
