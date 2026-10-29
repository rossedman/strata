// Package testreg runs an in-memory OCI registry seeded with a small,
// realistic image graph, for crawler, CLI and API tests.
package testreg

import (
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// Registry is a running test registry and the images pushed to it.
type Registry struct {
	Host   string
	Server *httptest.Server
	// Images maps a short name (debian, go-amd64, api, ...) to the image.
	Images map[string]v1.Image
	// GoIndex is the multi-platform index pushed as base/go:1.22.
	GoIndex v1.ImageIndex
	t       testing.TB
}

// Start runs a registry with referrers support and pushes:
//
//	base/debian:12                   one layer
//	base/go:1.22                     index: amd64 = debian + 1 layer, arm64 = unrelated
//	payments/api:1.0, :latest        go amd64 + 1 layer, labels, env, history
//	payments/worker:v1               go amd64 + 1 layer, OCI base annotations
//	payments/api:sha256-<api>.sig    a cosign signature
//	an SBOM referrer for api, through the referrers API
func Start(t testing.TB) *Registry {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.WithReferrersSupport(true), registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	r := &Registry{Host: strings.TrimPrefix(srv.URL, "http://"), Server: srv, Images: map[string]v1.Image{}, t: t}

	debian := r.layered(empty.Image, "debian", time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), nil, "ADD rootfs /")
	r.Push("base/debian:12", debian)

	goAMD := r.layered(debian, "go-amd64", time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC), nil, "RUN install go")
	goARM := r.layered(empty.Image, "go-arm64", time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC), nil, "ADD arm rootfs /", "RUN install go")
	goARM = r.platform(goARM, "linux", "arm64", "v8")
	r.Images["go-arm64"] = goARM
	r.GoIndex = mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{Add: goAMD, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}},
		mutate.IndexAddendum{Add: goARM, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}}},
	)
	r.PushIndex("base/go:1.22", r.GoIndex)

	api := r.layered(goAMD, "api", time.Date(2025, 8, 1, 0, 0, 0, 0, time.UTC), &v1.Config{
		Labels:       map[string]string{"team": "payments", "org.opencontainers.image.source": "https://github.com/acme/api"},
		Env:          []string{"PATH=/usr/bin", "GO_VERSION=1.22", "AWS_SECRET_ACCESS_KEY=hunter2"},
		User:         "root",
		ExposedPorts: map[string]struct{}{"8080/tcp": {}, "22/tcp": {}},
		Entrypoint:   []string{"/bin/sh", "-c", "/start.sh"},
		Healthcheck:  &v1.HealthConfig{Test: []string{"CMD", "true"}},
	}, "RUN TOKEN=abc123 ./fetch https://user:pw@example.com/x")
	r.Push("payments/api:1.0", api)
	r.Push("payments/api:latest", api)

	worker := r.layered(goAMD, "worker", time.Date(2025, 8, 2, 0, 0, 0, 0, time.UTC), &v1.Config{Labels: map[string]string{"team": "payments"}}, "RUN build worker")
	worker = mutate.Annotations(worker, map[string]string{
		"org.opencontainers.image.base.name": r.Host + "/base/go:1.22",
	}).(v1.Image)
	r.Images["worker"] = worker
	r.Push("payments/worker:v1", worker)

	apiDigest, err := api.Digest()
	if err != nil {
		t.Fatal(err)
	}
	sig := r.layered(empty.Image, "sig", time.Time{}, nil, "")
	r.Push("payments/api:sha256-"+apiDigest.Hex+".sig", sig)

	sbom := mutate.ConfigMediaType(r.layered(empty.Image, "sbom", time.Time{}, nil, ""), "application/spdx+json")
	sbomWithSubject := mutate.Subject(sbom, v1.Descriptor{MediaType: types.OCIManifestSchema1, Digest: apiDigest, Size: r.size(api)}).(v1.Image)
	sd, err := sbomWithSubject.Digest()
	if err != nil {
		t.Fatal(err)
	}
	r.Push("payments/api@"+sd.String(), sbomWithSubject)
	return r
}

// layered appends one random layer per history command to base, with the
// given creation time and config.
func (r *Registry) layered(base v1.Image, short string, created time.Time, cfg *v1.Config, cmds ...string) v1.Image {
	r.t.Helper()
	img := mutate.MediaType(base, types.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, types.OCIConfigJSON)
	for _, cmd := range cmds {
		l, err := random.Layer(256, types.OCILayer)
		if err != nil {
			r.t.Fatal(err)
		}
		if img, err = mutate.Append(img, mutate.Addendum{Layer: l, History: v1.History{CreatedBy: cmd, Created: v1.Time{Time: created}}}); err != nil {
			r.t.Fatal(err)
		}
	}
	cf, err := img.ConfigFile()
	if err != nil {
		r.t.Fatal(err)
	}
	cf = cf.DeepCopy()
	if cf.OS == "" {
		cf.OS, cf.Architecture = "linux", "amd64"
	}
	if cfg != nil {
		cf.Config = *cfg
	}
	cf.Created = v1.Time{Time: created}
	if img, err = mutate.ConfigFile(img, cf); err != nil {
		r.t.Fatal(err)
	}
	r.Images[short] = img
	return img
}

// platform sets an image's OS, architecture and variant.
func (r *Registry) platform(img v1.Image, os, arch, variant string) v1.Image {
	r.t.Helper()
	cf, err := img.ConfigFile()
	if err != nil {
		r.t.Fatal(err)
	}
	cf = cf.DeepCopy()
	cf.OS, cf.Architecture, cf.Variant = os, arch, variant
	out, err := mutate.ConfigFile(img, cf)
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

// size returns the size of an image's manifest.
func (r *Registry) size(img v1.Image) int64 {
	r.t.Helper()
	n, err := img.Size()
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

// Ref parses a ref relative to the registry, such as payments/api:1.0.
func (r *Registry) Ref(s string) name.Reference {
	r.t.Helper()
	ref, err := name.ParseReference(r.Host+"/"+s, name.Insecure)
	if err != nil {
		r.t.Fatal(err)
	}
	return ref
}

// Push writes an image to repo:tag or repo@digest.
func (r *Registry) Push(ref string, img v1.Image) {
	r.t.Helper()
	if err := remote.Write(r.Ref(ref), img); err != nil {
		r.t.Fatal(err)
	}
}

// PushIndex writes an index to repo:tag.
func (r *Registry) PushIndex(ref string, idx v1.ImageIndex) {
	r.t.Helper()
	if err := remote.WriteIndex(r.Ref(ref), idx); err != nil {
		r.t.Fatal(err)
	}
}

// Delete removes a tag or manifest.
func (r *Registry) Delete(ref string) {
	r.t.Helper()
	if err := remote.Delete(r.Ref(ref)); err != nil {
		r.t.Fatal(err)
	}
}

// Layered builds a new image on top of a named one, for tests that push
// more images after Start.
func (r *Registry) Layered(base, short string, created time.Time, cfg *v1.Config, cmds ...string) v1.Image {
	return r.layered(r.Images[base], short, created, cfg, cmds...)
}

// Digest returns a named image's digest.
func (r *Registry) Digest(short string) string {
	r.t.Helper()
	d, err := r.Images[short].Digest()
	if err != nil {
		r.t.Fatal(err)
	}
	return d.String()
}
