package web

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teamtito/starwavatars/internal/avatar"
)

// Every case here was checked with curl against the live Rails service
// (starwavatars.herokuapp.com) on 2026-11-06; the comment says what live
// did where it is not obvious.
var cases = []struct {
	method, path string
	header       map[string]string
	status       int
	contentType  string
	body         string // "svg:<id>" or "png:<id>" for an avatar, else exact
}{
	{"GET", "/avatars/abc", nil, 200, "image/svg+xml; charset=utf-8", "svg:abc"},
	{"GET", "/avatars/abc.svg", nil, 200, "image/svg+xml; charset=utf-8", "svg:abc"},
	{"GET", "/avatars/abc.png", nil, 200, "image/png", "png:abc"},
	{"GET", "/avatars/abc.png?x=1", nil, 200, "image/png", "png:abc"},
	{"GET", "/avatars/abc?format=png", nil, 200, "image/svg+xml; charset=utf-8", "svg:abc"}, // the path's default wins
	{"GET", "/avatars/%C3%A9", nil, 200, "image/svg+xml; charset=utf-8", "svg:é"},
	{"GET", "/avatars/%C3%A9.png", nil, 200, "image/png", "png:é"},
	{"GET", "/avatars/a%2Fb", nil, 200, "image/svg+xml; charset=utf-8", "svg:a/b"},
	{"GET", "/avatars/a%2Epng", nil, 200, "image/svg+xml; charset=utf-8", "svg:a.png"},
	{"GET", "/avatars/a%2epng", nil, 200, "image/svg+xml; charset=utf-8", "svg:a.png"},
	{"GET", "/avatars/a+b", nil, 200, "image/svg+xml; charset=utf-8", "svg:a+b"},
	{"GET", "/avatars/a%20b", nil, 200, "image/svg+xml; charset=utf-8", "svg:a b"},
	{"GET", "/avatars/%00", nil, 200, "image/svg+xml; charset=utf-8", "svg:\x00"},
	{"GET", "/avatars/a%25", nil, 200, "image/svg+xml; charset=utf-8", "svg:a%"},
	{"GET", "/avatars/a%3Fb", nil, 200, "image/svg+xml; charset=utf-8", "svg:a?b"},
	{"GET", "/avatars/abc%2F", nil, 200, "image/svg+xml; charset=utf-8", "svg:abc/"},
	{"GET", "/avatars/abc/", nil, 200, "image/svg+xml; charset=utf-8", "svg:abc"},
	{"GET", "/avatars/abc.svg/", nil, 200, "image/svg+xml; charset=utf-8", "svg:abc"},
	{"GET", "/avatars//abc", nil, 200, "image/svg+xml; charset=utf-8", "svg:abc"},
	{"GET", "//avatars/abc", nil, 200, "image/svg+xml; charset=utf-8", "svg:abc"},

	// Rails' public/ files.
	{"GET", "/robots.txt", nil, 200, "text/plain", "robots"},
	{"GET", "/favicon.ico", nil, 200, "image/vnd.microsoft.icon", ""},
	{"GET", "/422.html", nil, 404, "text/html; charset=utf-8", "404"}, // live serves it; nobody needs it

	// No route.
	{"GET", "/avatars/a.b.png", nil, 404, "text/html; charset=utf-8", "404"},
	{"GET", "/avatars/a.b.svg", nil, 404, "text/html; charset=utf-8", "404"},
	{"GET", "/avatars/abc.", nil, 404, "text/html; charset=utf-8", "404"},
	{"GET", "/avatars/abc.png.png", nil, 404, "text/html; charset=utf-8", "404"},
	{"GET", "/avatars/.png", nil, 404, "text/html; charset=utf-8", "404"},
	{"GET", "/avatars/", nil, 404, "text/html; charset=utf-8", "404"},
	{"GET", "/avatars", nil, 404, "text/html; charset=utf-8", "404"},
	{"GET", "/avatars/abc/def", nil, 404, "text/html; charset=utf-8", "404"},
	{"GET", "/AVATARS/abc", nil, 404, "text/html; charset=utf-8", "404"},
	{"GET", "/", nil, 404, "text/html; charset=utf-8", "404"},
	{"GET", "/foo", nil, 404, "text/html; charset=utf-8", "404"},
	{"GET", "/foo.json", nil, 404, "text/html; charset=utf-8", "404"}, // no route, so no path format
	{"POST", "/avatars/abc", nil, 404, "text/html; charset=utf-8", "404"},
	{"DELETE", "/avatars/abc", nil, 404, "text/html; charset=utf-8", "404"},
	{"OPTIONS", "/avatars/abc", nil, 404, "text/html; charset=utf-8", "404"},
	{"GET", "/foo?format=json", nil, 404, "application/json; charset=utf-8", `{"status":"404","error":"Not Found"}`},
	{"GET", "/foo", map[string]string{"Accept": "application/json"}, 404, "application/json; charset=utf-8", `{"status":"404","error":"Not Found"}`},
	{"GET", "/foo", map[string]string{"Accept": "text/html,application/xhtml+xml,*/*;q=0.8"}, 404, "text/html; charset=utf-8", "404"},
	{"GET", "/foo", map[string]string{"Accept": "text/plain;q=0.5, application/xml"}, 404, "application/xml; charset=utf-8",
		"<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<hash>\n  <status>404</status>\n  <error>Not Found</error>\n</hash>\n"},

	// Formats the controller does not respond to.
	{"GET", "/avatars/a.b", nil, 406, "text/html; charset=utf-8", ""},
	{"GET", "/avatars/a.b", map[string]string{"Accept": "application/json"}, 406, "text/html; charset=utf-8", ""}, // path format wins
	{"GET", "/avatars/a.b?format=json", nil, 406, "text/html; charset=utf-8", ""},
	{"GET", "/avatars/abc.PNG", nil, 406, "text/html; charset=utf-8", ""},
	{"GET", "/avatars/abc.SVG", nil, 406, "text/html; charset=utf-8", ""},
	{"GET", "/avatars/abc.html", nil, 406, "text/html; charset=utf-8", ""},
	{"GET", "/avatars/abc.jpg", nil, 406, "text/html; charset=utf-8", ""},
	{"GET", "/avatars/abc.gif", nil, 406, "text/html; charset=utf-8", ""},
	{"GET", "/avatars/abc.yml", nil, 406, "text/html; charset=utf-8", ""},
	{"GET", "/avatars/abc.xyz", nil, 406, "text/html; charset=utf-8", ""},
	{"GET", "/avatars/abc.json", nil, 406, "application/json; charset=utf-8", `{"status":"406","error":"Not Acceptable"}`},
	{"GET", "/avatars/abc.xml", nil, 406, "application/xml; charset=utf-8",
		"<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<hash>\n  <status>406</status>\n  <error>Not Acceptable</error>\n</hash>\n"},
	{"GET", "/avatars/abc.yaml", nil, 406, "application/x-yaml; charset=utf-8", "---\n:status: '406'\n:error: Not Acceptable\n"},

	// Not UTF-8 once decoded.
	{"GET", "/avatars/%FF", nil, 400, "text/html; charset=utf-8", ""},
	{"GET", "/avatars/%C3", nil, 400, "text/html; charset=utf-8", ""},
	{"GET", "/avatars/%FF", map[string]string{"Accept": "application/json"}, 400, "text/html; charset=utf-8", ""},
}

func TestRoutesMatchRails(t *testing.T) {
	srv := httptest.NewServer(Handler("test"))
	defer srv.Close()
	for _, tc := range cases {
		name := tc.method + " " + tc.path
		if tc.header != nil {
			name += fmt.Sprint(" ", tc.header)
		}
		t.Run(name, func(t *testing.T) {
			status, hdr, body := rawRequest(t, srv, tc.method, tc.path, tc.header)
			if status != tc.status {
				t.Fatalf("status %d, want %d", status, tc.status)
			}
			if ct := hdr.Get("Content-Type"); ct != tc.contentType {
				t.Errorf("Content-Type %q, want %q", ct, tc.contentType)
			}
			want := []byte(tc.body)
			switch {
			case tc.body == "404":
				want = notFoundPage
			case tc.body == "robots":
				want = robotsTxt
			case strings.HasPrefix(tc.body, "svg:"):
				want, _ = avatar.New(tc.body[4:]).SVG()
			case strings.HasPrefix(tc.body, "png:"):
				if !bytes.HasPrefix(body, []byte("\x89PNG")) {
					t.Fatalf("not a PNG: %q", body[:min(len(body), 16)])
				}
				want = body
				id := tc.body[4:]
				if cd := hdr.Get("Content-Disposition"); cd != `inline; filename="`+id+`.png"` {
					t.Errorf("Content-Disposition %q", cd)
				}
			}
			if !bytes.Equal(body, want) {
				t.Errorf("body %q, want %q", trim(body), trim(want))
			}
			if strings.HasPrefix(tc.body, "svg:") || strings.HasPrefix(tc.body, "png:") {
				if cc := hdr.Get("Cache-Control"); cc != CacheControl {
					t.Errorf("Cache-Control %q", cc)
				}
			}
		})
	}
}

func TestLiveNotFoundPage(t *testing.T) {
	// Rails served public/404.html for a missing route; it is the same file.
	if !bytes.Contains(notFoundPage, []byte("The page you were looking for doesn't exist (404)")) || len(notFoundPage) != 1564 {
		t.Errorf("404 page is not Rails' public/404.html (%d bytes)", len(notFoundPage))
	}
}

func TestMalformedEscapeIsBadRequest(t *testing.T) {
	// Live's Heroku front end resets the stream; Go's server answers 400
	// before the handler runs, which is as close as it gets.
	srv := httptest.NewServer(Handler("test"))
	defer srv.Close()
	for _, p := range []string{"/avatars/%zz", "/avatars/%2"} {
		if status, _, _ := rawRequest(t, srv, "GET", p, nil); status != 400 {
			t.Errorf("%s: status %d, want 400", p, status)
		}
	}
}

func TestUp(t *testing.T) {
	srv := httptest.NewServer(Handler("abc123"))
	defer srv.Close()
	status, hdr, body := rawRequest(t, srv, "GET", "/up", nil)
	if status != 200 || string(body) != "ok abc123\n" {
		t.Errorf("/up = %d %q", status, body)
	}
	if hdr.Get("Cache-Control") != "no-store" {
		t.Errorf("/up Cache-Control %q", hdr.Get("Cache-Control"))
	}
}

func TestHeadAndConditionalGet(t *testing.T) {
	srv := httptest.NewServer(Handler("test"))
	defer srv.Close()
	svg, _ := avatar.New("abc").SVG()

	// Rails' Rack::ETag: a weak tag of the body's md5.
	_, hdr, _ := rawRequest(t, srv, "GET", "/avatars/abc", nil)
	if etag := hdr.Get("ETag"); etag != `W/"3d9bbdaed09bd1d36a001d103bfaf7f1"` {
		t.Errorf("ETag %q; live sends W/\"3d9bbdaed09bd1d36a001d103bfaf7f1\" for abc", etag)
	}
	if hdr.Get("X-Content-Type-Options") != "nosniff" || hdr.Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Errorf("security headers: %v", hdr)
	}

	status, hdr, body := rawRequest(t, srv, "HEAD", "/avatars/abc", nil)
	if status != 200 || len(body) != 0 || hdr.Get("Content-Length") != fmt.Sprint(len(svg)) {
		t.Errorf("HEAD = %d, %d bytes, Content-Length %q", status, len(body), hdr.Get("Content-Length"))
	}

	status, hdr, body = rawRequest(t, srv, "GET", "/avatars/abc", map[string]string{"If-None-Match": `W/"3d9bbdaed09bd1d36a001d103bfaf7f1"`})
	if status != 304 || len(body) != 0 || hdr.Get("Cache-Control") != CacheControl {
		t.Errorf("conditional GET = %d, %d bytes, %v", status, len(body), hdr)
	}
}

func TestControlCharactersInFilename(t *testing.T) {
	// Live answers 503 here: Rails put the raw id in the header and
	// Heroku's router refused the response.
	srv := httptest.NewServer(Handler("test"))
	defer srv.Close()
	status, hdr, _ := rawRequest(t, srv, "GET", "/avatars/a%0D%0Ab%00c%09d.png", nil)
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	if cd := hdr.Get("Content-Disposition"); cd != "inline; filename=\"a__b_c\td.png\"" {
		t.Errorf("Content-Disposition %q", cd)
	}
	if hdr.Get("Content-Transfer-Encoding") != "binary" {
		t.Errorf("Content-Transfer-Encoding %q", hdr.Get("Content-Transfer-Encoding"))
	}
}

func TestNormalizePath(t *testing.T) {
	for in, want := range map[string]string{
		"":                 "/",
		"/":                "/",
		"//":               "/",
		"/avatars/abc/":    "/avatars/abc",
		"//avatars//abc//": "/avatars/abc",
		"avatars":          "/avatars",
	} {
		if got := normalizePath(in); got != want {
			t.Errorf("normalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// rawRequest sends the request line exactly as given, so paths Go's client
// would clean or re-escape reach the server as they would from curl.
func rawRequest(t *testing.T, srv *httptest.Server, method, path string, header map[string]string) (int, http.Header, []byte) {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var req strings.Builder
	fmt.Fprintf(&req, "%s %s HTTP/1.1\r\nHost: test\r\nConnection: close\r\n", method, path)
	for k, v := range header {
		fmt.Fprintf(&req, "%s: %s\r\n", k, v)
	}
	req.WriteString("\r\n")
	if _, err := io.WriteString(conn, req.String()); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: method})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, body
}

func trim(b []byte) string {
	if len(b) > 80 {
		return string(b[:80]) + "..."
	}
	return string(b)
}
