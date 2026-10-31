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
				"accessories":[{"digest":"sha256:sig","type":"signature.cosign","subject_artifact_digest":"sha256:a"},{"digest":"sha256:sbom","type":"harbor.sbom"}]},
				{"digest":"sha256:b","manifest_media_type":"application/vnd.oci.image.manifest.v1+json","push_time":"2026-01-01T00:00:00Z","pull_time":"2026-02-01T00:00:00Z"}]`))
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
	if imgs[1].Subject != "sha256:a" || model.ClassifyArtifact(imgs[1].ArtifactType) != model.KindSignature ||
		imgs[2].Subject != "sha256:a" || model.ClassifyArtifact(imgs[2].ArtifactType) != model.KindSBOM {
		t.Fatalf("accessories = %+v %+v", imgs[1], imgs[2])
	}
	if b := imgs[3]; b.LastPulled.Month() != 2 {
		t.Fatalf("b = %+v", b)
	}

	for _, f := range []func() error{
		func() error { _, err := r.Images(ctx, "team/missing"); return err },
		func() error { _, err := r.Images(ctx, "noproject"); return err },
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
