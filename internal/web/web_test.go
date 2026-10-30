package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	t.Parallel()
	h := Handler()
	get := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}
	for _, p := range []string{"/", "/index.html", "/lineage/deep/link"} {
		w := get(http.MethodGet, p)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<script src="app.js">`) || w.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("GET %s = %d %q", p, w.Code, w.Header())
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'self'") {
			t.Errorf("GET %s has no CSP", p)
		}
	}
	if w := get(http.MethodHead, "/"); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("HEAD / = %d, %d bytes", w.Code, w.Body.Len())
	}
	if w := get(http.MethodGet, "/app.js"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "v1/outline") {
		t.Fatalf("app.js = %d", w.Code)
	}
	if w := get(http.MethodGet, "/fonts/Inter-Variable.woff2"); w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("font = %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
}
