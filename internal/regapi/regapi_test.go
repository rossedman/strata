package regapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGet(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			if r.Header.Get("X-Auth") != "yes" || r.Header.Get("User-Agent") != "strata/test" {
				http.Error(w, "no auth", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Link", `</b?page=2>; rel="next"`)
			_, _ = w.Write([]byte(`{"n":1}`))
		case "/b":
			_, _ = w.Write([]byte(`{"n":2}`))
		case "/bad":
			_, _ = w.Write([]byte(`not json`))
		case "/badlink":
			w.Header().Set("Link", `<:%zz>; rel=next`)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Client{UserAgent: "strata/test", Auth: func(r *http.Request) error { r.Header.Set("X-Auth", "yes"); return nil }}
	var out struct{ N int }
	next, err := c.Get(context.Background(), srv.URL+"/a", &out)
	if err != nil || out.N != 1 || next != srv.URL+"/b?page=2" {
		t.Fatalf("a = %v, %q, %v", out, next, err)
	}
	if next, err = c.Get(context.Background(), next, &out); err != nil || out.N != 2 || next != "" {
		t.Fatalf("b = %v, %q, %v", out, next, err)
	}
	var se *StatusError
	if _, err := c.Get(context.Background(), srv.URL+"/missing", &out); !errors.As(err, &se) || se.Code != http.StatusNotFound || se.Error() == "" {
		t.Fatalf("missing = %v", err)
	}
	if _, err := c.Get(context.Background(), srv.URL+"/bad", &out); err == nil {
		t.Fatal("bad JSON decoded")
	}
	if _, err := c.Get(context.Background(), srv.URL+"/badlink", &out); err == nil {
		t.Fatal("bad link parsed")
	}
	failing := &Client{Auth: func(*http.Request) error { return errors.New("no token") }}
	if _, err := failing.Get(context.Background(), srv.URL+"/b", &out); err == nil {
		t.Fatal("auth error ignored")
	}
	if _, err := c.Get(context.Background(), "http://127.0.0.1:1/x", &out); err == nil {
		t.Fatal("unreachable server succeeded")
	}
	if _, err := c.Get(context.Background(), "::", &out); err == nil {
		t.Fatal("bad URL accepted")
	}
	if c.Calls() != 6 || Scheme(true) != "http" || Scheme(false) != "https" {
		t.Fatalf("calls = %d", c.Calls())
	}
}
