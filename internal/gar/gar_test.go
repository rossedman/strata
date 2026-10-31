package gar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rossedman/strata/internal/model"
)

const host = "us-docker.pkg.dev"

// fakeAPIs serves the Artifact Registry and Container Analysis calls.
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
		case "/ca/projects/acme/occurrences":
			f := r.URL.Query().Get("filter")
			switch {
			case strings.Contains(f, "sha256:1"):
				if tok == "" {
					_, _ = w.Write([]byte(`{"occurrences":[{"kind":"DISCOVERY","discovery":{"analysisStatus":"FINISHED_SUCCESS","lastScanTime":"2026-01-05T00:00:00Z"}}],"nextPageToken":"n"}`))
					return
				}
				_, _ = w.Write([]byte(`{"occurrences":[
					{"kind":"VULNERABILITY","vulnerability":{"effectiveSeverity":"CRITICAL","shortDescription":"CVE-2024-3094","relatedUrls":[{"url":"https://x"}],
						"packageIssue":[{"affectedPackage":"xz-utils","affectedVersion":{"fullName":"5.6.0"},"fixedVersion":{"fullName":"5.6.1","kind":"NORMAL"}}]}},
					{"kind":"VULNERABILITY","vulnerability":{"effectiveSeverity":"SEVERITY_UNSPECIFIED","severity":"LOW","shortDescription":"CVE-1",
						"packageIssue":[{"affectedPackage":"zlib","affectedVersion":{"fullName":"1"},"fixedVersion":{"kind":"MAXIMUM"}}]}},
					{"kind":"VULNERABILITY","vulnerability":{"effectiveSeverity":"HIGH","shortDescription":"CVE-2"}}]}`))
			case strings.Contains(f, "sha256:2"):
				_, _ = w.Write([]byte(`{"occurrences":[{"kind":"DISCOVERY","discovery":{"analysisStatus":"PENDING"}}]}`))
			case strings.Contains(f, "sha256:3"):
				_, _ = w.Write([]byte(`{}`))
			default:
				http.Error(w, "boom", http.StatusInternalServerError)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func client(t *testing.T, srv *httptest.Server, token func() (string, error)) *Registry {
	t.Helper()
	r, err := New(host, Options{Projects: []string{"acme"}, Token: token, RegistryAPI: srv.URL + "/ar", AnalysisAPI: srv.URL + "/ca", UserAgent: "strata/test"})
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

	sc, err := r.Scan(ctx, "acme/apps/api", "sha256:1")
	if err != nil {
		t.Fatal(err)
	}
	if sc.Status != model.ScanComplete || sc.CompletedAt.Day() != 5 || sc.Critical != 1 || sc.High != 1 || sc.Low != 1 || !sc.HasFindings || len(sc.Findings) != 3 {
		t.Fatalf("scan = %+v", sc)
	}
	if f := sc.Findings[0]; f != (model.Finding{ID: "CVE-2024-3094", Package: "xz-utils", Version: "5.6.0", Severity: "critical", FixedIn: "5.6.1", URI: "https://x"}) {
		t.Fatalf("finding = %+v", f)
	}
	if sc.Findings[1].FixedIn != "" || sc.Findings[2].Package != "" {
		t.Fatalf("findings = %+v", sc.Findings)
	}
	if sc, err := r.Scan(ctx, "acme/apps/team/web", "sha256:2"); err != nil || sc.Status != "pending" {
		t.Fatalf("pending scan = %+v, %v", sc, err)
	}
	if sc, err := r.Scan(ctx, "acme/apps/api", "sha256:3"); err != nil || sc.Status != model.ScanNone {
		t.Fatalf("unscanned = %+v, %v", sc, err)
	}
	if _, err := r.Scan(ctx, "acme/apps/api", "sha256:9"); err == nil {
		t.Fatal("failed scan read succeeded")
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
	if def.opts.RegistryAPI != ArtifactRegistryAPI || def.opts.AnalysisAPI != ContainerAnalysisAPI {
		t.Fatal("default endpoints not set")
	}
}
