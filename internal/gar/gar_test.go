package gar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const host = "us-docker.pkg.dev"

// fakeAPIs serves the Artifact Registry calls.
func fakeAPIs(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		tok := r.URL.Query().Get("pageToken")
		switch r.URL.Path {
		case "/ar/projects/acme/locations/us/repositories":
			_, _ = w.Write([]byte(`{"repositories":[{"name":"projects/acme/locations/us/repositories/apps","format":"DOCKER"},{"name":"projects/acme/locations/us/repositories/npm","format":"NPM"}]}`))
		case "/ar/projects/acme/locations/us/repositories/apps/dockerImages":
			if tok == "" {
				_, _ = w.Write([]byte(`{"dockerImages":[
					{"uri":"us-docker.pkg.dev/acme/apps/api@sha256:1","tags":["v1"],"mediaType":"application/vnd.oci.image.manifest.v1+json","uploadTime":"2026-01-02T00:00:00Z"},
					{"uri":"us-docker.pkg.dev/acme/apps/team/web@sha256:2","mediaType":"application/vnd.oci.image.manifest.v1+json","uploadTime":"2026-01-03T00:00:00Z"},
					{"uri":"bogus"}],"nextPageToken":"p2"}`))
				return
			}
			_, _ = w.Write([]byte(`{"dockerImages":[{"uri":"us-docker.pkg.dev/acme/apps/api@sha256:3","tags":["v2","latest"],"uploadTime":"2026-01-04T00:00:00Z"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func client(t *testing.T, srv *httptest.Server, token func() (string, error)) *Registry {
	t.Helper()
	r, err := New(host, Options{Projects: []string{"acme"}, Token: token, RegistryAPI: srv.URL + "/ar", UserAgent: "strata/test"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGAR(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := fakeAPIs(t)
	r := client(t, srv, func() (string, error) { return "tok", nil })
	repos, err := r.Repos(ctx)
	if err != nil || strings.Join(repos, ",") != "acme/apps/api,acme/apps/team/web" {
		t.Fatalf("repos = %v, %v", repos, err)
	}
	api, err := r.Images(ctx, "acme/apps/api")
	if err != nil || len(api) != 2 || api[0].Digest != "sha256:1" || api[0].Tags[0] != "v1" || api[0].PushedAt.Day() != 2 || len(api[1].Tags) != 2 {
		t.Fatalf("api = %+v, %v", api, err)
	}
	// Each image path is handed out once.
	if _, err := r.Images(ctx, "acme/apps/api"); err == nil {
		t.Fatal("api listed twice")
	}

	// A seeded repo lists its repository without Repos.
	seeded := client(t, srv, func() (string, error) { return "tok", nil })
	if web, err := seeded.Images(ctx, "acme/apps/team/web"); err != nil || len(web) != 1 || web[0].Digest != "sha256:2" {
		t.Fatalf("seeded web = %+v, %v", web, err)
	}

	for _, path := range []string{"acme/apps", "acme/missing/x", "acme/apps/nothing"} {
		if _, err := r.Images(ctx, path); err == nil {
			t.Errorf("Images(%s) succeeded", path)
		}
	}
	if r.Calls() == 0 {
		t.Fatal("calls not counted")
	}
	bad := client(t, srv, func() (string, error) { return "", errors.New("no credentials") })
	if _, err := bad.Repos(ctx); err == nil || !strings.Contains(err.Error(), "no credentials") {
		t.Fatalf("token error = %v", err)
	}
	anon := client(t, srv, nil)
	if _, err := anon.Repos(ctx); err == nil {
		t.Fatal("anonymous listing succeeded")
	}
	if _, err := New("gcr.io", Options{}); err == nil {
		t.Fatal("non-GAR host accepted")
	}
	if loc, ok := ParseHost("europe-west1-docker.pkg.dev"); !ok || loc != "europe-west1" {
		t.Fatalf("ParseHost = %q, %v", loc, ok)
	}
	def, _ := New(host, Options{})
	if def.opts.RegistryAPI != ArtifactRegistryAPI {
		t.Fatal("default endpoints not set")
	}
}
