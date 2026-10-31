package harbor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"

	"github.com/rossedman/strata/internal/model"
)

// fakeHarbor serves the Harbor endpoints strata reads.
func fakeHarbor(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "robot$strata" || p != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.EscapedPath() {
		case "/api/v2.0/repositories":
			if r.URL.Query().Get("page") == "2" {
				_, _ = w.Write([]byte(`[{"name":"team/web/site"}]`))
				return
			}
			w.Header().Set("Link", `</api/v2.0/repositories?page=2&page_size=100>; rel="next"`)
			_, _ = w.Write([]byte(`[{"name":"team/api"}]`))
		case "/api/v2.0/projects/team/repositories/web%252Fsite/artifacts":
			_, _ = w.Write([]byte(`[{"digest":"sha256:a","manifest_media_type":"application/vnd.oci.image.manifest.v1+json",
				"push_time":"2026-01-02T00:00:00Z","pull_time":"0001-01-01T00:00:00Z","tags":[{"name":"v1"},{"name":"latest"}],
				"scan_overview":{"application/vnd.security.vulnerability.report; version=1.1":{"scan_status":"Success","end_time":"2026-01-03T00:00:00Z",
					"summary":{"total":4,"fixable":1,"summary":{"Critical":1,"High":2,"Unknown":1}}}},
				"accessories":[{"digest":"sha256:sig","type":"signature.cosign","subject_artifact_digest":"sha256:a"},{"digest":"sha256:sbom","type":"harbor.sbom"}]},
				{"digest":"sha256:b","manifest_media_type":"application/vnd.oci.image.manifest.v1+json","push_time":"2026-01-01T00:00:00Z","pull_time":"2026-02-01T00:00:00Z",
				"scan_overview":{"x":{"scan_status":"Running"}}}]`))
		case "/api/v2.0/projects/team/repositories/web%252Fsite/artifacts/sha256:a":
			_, _ = w.Write([]byte(`{"digest":"sha256:a","scan_overview":{"x":{"scan_status":"Success","end_time":"2026-01-03T00:00:00Z","summary":{"summary":{"Critical":1}}}}}`))
		case "/api/v2.0/projects/team/repositories/web%252Fsite/artifacts/sha256:a/additions/vulnerabilities":
			_, _ = w.Write([]byte(`{"x":{"vulnerabilities":[{"id":"CVE-2024-3094","package":"xz-utils","version":"5.6.0","fix_version":"5.6.1","severity":"Critical","links":["https://nvd/x"]}]}}`))
		case "/api/v2.0/projects/team/repositories/web%252Fsite/artifacts/sha256:b":
			_, _ = w.Write([]byte(`{"digest":"sha256:b","scan_overview":{"x":{"scan_status":"Running"}}}`))
		case "/api/v2.0/projects/team/repositories/web%252Fsite/artifacts/sha256:c":
			_, _ = w.Write([]byte(`{"digest":"sha256:c"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// robot is a keychain holding Harbor robot credentials.
type robot struct{}

func (robot) Resolve(authn.Resource) (authn.Authenticator, error) {
	return &authn.Basic{Username: "robot$strata", Password: "secret"}, nil
}

func TestHarbor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := fakeHarbor(t)
	r, err := New(strings.TrimPrefix(srv.URL, "http://"), true, robot{}, nil, "strata/test")
	if err != nil {
		t.Fatal(err)
	}
	repos, err := r.Repos(ctx)
	if err != nil || strings.Join(repos, ",") != "team/api,team/web/site" {
		t.Fatalf("repos = %v, %v", repos, err)
	}
	imgs, err := r.Images(ctx, "team/web/site")
	if err != nil || len(imgs) != 4 {
		t.Fatalf("images = %+v, %v", imgs, err)
	}
	a := imgs[0]
	if a.Digest != "sha256:a" || strings.Join(a.Tags, ",") != "v1,latest" || a.PushedAt.Day() != 2 || !a.LastPulled.IsZero() {
		t.Fatalf("a = %+v", a)
	}
	if sc := a.Scan; sc == nil || sc.Status != model.ScanComplete || sc.Critical != 1 || sc.High != 2 || sc.Other != 1 || sc.CompletedAt.Day() != 3 {
		t.Fatalf("scan = %+v", a.Scan)
	}
	if imgs[1].Subject != "sha256:a" || model.ClassifyArtifact(imgs[1].ArtifactType) != model.KindSignature ||
		imgs[2].Subject != "sha256:a" || model.ClassifyArtifact(imgs[2].ArtifactType) != model.KindSBOM {
		t.Fatalf("accessories = %+v %+v", imgs[1], imgs[2])
	}
	if b := imgs[3]; b.LastPulled.Month() != 2 || b.Scan.Status != "running" {
		t.Fatalf("b = %+v", b)
	}

	sc, err := r.Scan(ctx, "team/web/site", "sha256:a")
	if err != nil || !sc.HasFindings || sc.Digest != "sha256:a" || len(sc.Findings) != 1 ||
		sc.Findings[0] != (model.Finding{ID: "CVE-2024-3094", Package: "xz-utils", Version: "5.6.0", Severity: "critical", FixedIn: "5.6.1", URI: "https://nvd/x"}) {
		t.Fatalf("scan a = %+v, %v", sc, err)
	}
	if sc, err := r.Scan(ctx, "team/web/site", "sha256:b"); err != nil || sc.Status != "running" || len(sc.Findings) != 0 {
		t.Fatalf("scan b = %+v, %v", sc, err)
	}
	if sc, err := r.Scan(ctx, "team/web/site", "sha256:c"); err != nil || sc.Status != model.ScanNone {
		t.Fatalf("scan c = %+v, %v", sc, err)
	}
	for _, f := range []func() error{
		func() error { _, err := r.Images(ctx, "team/missing"); return err },
		func() error { _, err := r.Images(ctx, "noproject"); return err },
		func() error { _, err := r.Scan(ctx, "team/web/site", "sha256:missing"); return err },
		func() error { _, err := r.Scan(ctx, "noproject", "sha256:a"); return err },
	} {
		if f() == nil {
			t.Error("expected an error")
		}
	}
	if r.Calls() == 0 {
		t.Fatal("calls not counted")
	}
	// Without credentials every call fails.
	anon, err := New(strings.TrimPrefix(srv.URL, "http://"), true, authn.NewMultiKeychain(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := anon.Repos(ctx); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("anonymous repos = %v", err)
	}
	if _, err := New("bad host!", false, nil, nil, ""); err == nil {
		t.Fatal("bad host accepted")
	}
}
