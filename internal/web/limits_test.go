package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A quote in the id cannot close the filename and add a parameter of its
// own: Content-Disposition keeps exactly one, filename.
func TestFilenameCannotInjectParameters(t *testing.T) {
	srv := httptest.NewServer(Handler("test"))
	defer srv.Close()
	status, hdr, _ := rawRequest(t, srv, "GET", "/avatars/x%22%3B%20filename%2A%3DUTF-8%27%27evil%2Ehtml%3B%20x%3D%5C%22.png", nil)
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	cd := hdr.Get("Content-Disposition")
	if strings.Count(cd, `"`) != 2 || strings.Contains(cd, `\`) {
		t.Errorf("Content-Disposition %q, want one quoted filename with no quote or backslash inside", cd)
	}
}

// With every drawing slot taken, a PNG request waits, then is told to come
// back later; once a slot frees, it is drawn.
func TestPNGRendersAreBounded(t *testing.T) {
	h := newHandler("test", 1, 20*time.Millisecond)
	h.renders <- struct{}{}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatars/abc.png", nil))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("all slots taken: %d, Retry-After %q, want 503 with one", w.Code, w.Header().Get("Retry-After"))
	}
	<-h.renders
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatars/abc.png", nil))
	if w.Code != http.StatusOK || len(h.renders) != 0 {
		t.Fatalf("a free slot: %d with %d slots still held, want 200 and 0", w.Code, len(h.renders))
	}
	// An SVG is a string replace, not a drawing, and never waits.
	h.renders <- struct{}{}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatars/abc.svg", nil))
	if w.Code != http.StatusOK {
		t.Errorf("an SVG with every slot taken: %d, want 200", w.Code)
	}
}

// An id past maxID is refused before anything is drawn.
func TestALongIDIsRefused(t *testing.T) {
	h := newHandler("test", 1, time.Millisecond)
	for _, c := range []struct {
		n    int
		want int
	}{{maxID, http.StatusOK}, {maxID + 1, http.StatusRequestURITooLong}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/avatars/"+strings.Repeat("a", c.n)+".png", nil))
		if w.Code != c.want {
			t.Errorf("an id of %d: %d, want %d", c.n, w.Code, c.want)
		}
	}
}
