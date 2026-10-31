package acr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
)

// sp is a keychain holding service principal credentials.
type sp struct{}

func (sp) Resolve(authn.Resource) (authn.Authenticator, error) {
	return &authn.Basic{Username: "app-id", Password: "secret"}, nil
}

func TestACR(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "app-id" || p != "secret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="acr"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.EscapedPath() {
		case "/v2/":
			_, _ = w.Write([]byte(`{}`))
		case "/acr/v1/_catalog":
			if r.URL.Query().Get("last") == "" {
				w.Header().Set("Link", `</acr/v1/_catalog?last=team%2Fapi&n=1000>; rel="next"`)
				_, _ = w.Write([]byte(`{"repositories":["team/api"]}`))
				return
			}
			_, _ = w.Write([]byte(`{"repositories":["web"]}`))
		case "/acr/v1/team/api/_manifests":
			_, _ = w.Write([]byte(`{"manifests":[{"digest":"sha256:1","mediaType":"application/vnd.oci.image.manifest.v1+json","createdTime":"2026-01-02T00:00:00Z","tags":["v1","latest"]},
				{"digest":"sha256:2","createdTime":"2026-01-01T00:00:00Z"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	r, err := New(host, true, sp{}, nil, "strata/test")
	if err != nil {
		t.Fatal(err)
	}
	repos, err := r.Repos(ctx)
	if err != nil || strings.Join(repos, ",") != "team/api,web" {
		t.Fatalf("repos = %v, %v", repos, err)
	}
	imgs, err := r.Images(ctx, "team/api")
	if err != nil || len(imgs) != 2 || strings.Join(imgs[0].Tags, ",") != "v1,latest" || imgs[0].PushedAt.Day() != 2 || len(imgs[1].Tags) != 0 {
		t.Fatalf("images = %+v, %v", imgs, err)
	}
	if _, err := r.Images(ctx, "missing"); err == nil {
		t.Fatal("missing repo listed")
	}
	if r.Calls() != 4 {
		t.Fatalf("calls = %d", r.Calls())
	}
	anon, _ := New(host, true, authn.NewMultiKeychain(), nil, "")
	if _, err := anon.Repos(ctx); err == nil {
		t.Fatal("anonymous listing succeeded")
	}
	if _, err := New("bad host!", false, nil, nil, ""); err == nil {
		t.Fatal("bad host accepted")
	}
	for h, want := range map[string]bool{"acme.azurecr.io": true, "acme.azurecr.cn": true, "acme.azurecr.us": true, "reg.io": false} {
		if IsHost(h) != want {
			t.Errorf("IsHost(%s) = %v", h, !want)
		}
	}
	if escapePath("a b/c") != "a%20b/c" {
		t.Fatal("escapePath")
	}
}
