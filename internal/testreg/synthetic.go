package testreg

import (
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/sync/errgroup"
)

// Shape sizes a synthetic registry.
type Shape struct {
	// Repos is the number of application repos, split across teams.
	Repos int
	// Tags is the number of images per repo; each gets its own tag.
	Tags int
	// MultiArchEvery makes every Nth repo push amd64 and arm64 indexes
	// instead of single images. Zero means none.
	MultiArchEvery int
	// Bases is the number of base images applications build on.
	Bases int
}

// Synthetic holds a generated registry and what it contains.
type Synthetic struct {
	*Registry
	// RepoNames lists every repo, bases first.
	RepoNames []string
	// Tags counts every tag pushed; Manifests counts every platform
	// image, which is what a full crawl indexes; Indexes counts the
	// multi-platform indexes among the tags.
	Tags, Manifests, Indexes int
}

// StartSynthetic runs a registry without referrers support and fills it
// with a generated organization: base images, then repos of
// applications built on them, one small layer per image. It is sized for
// benchmarks, so it pushes concurrently.
func StartSynthetic(t testing.TB, s Shape) *Synthetic {
	t.Helper()
	if s.Bases < 1 {
		s.Bases = 1
	}
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	r := &Registry{Host: strings.TrimPrefix(srv.URL, "http://"), Server: srv, Images: map[string]v1.Image{}, t: t}
	syn := &Synthetic{Registry: r}
	epoch := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	bases := make([]v1.Image, s.Bases)
	for i := range bases {
		bases[i] = r.layered(empty.Image, fmt.Sprintf("base-%d", i), epoch, nil, "ADD rootfs /")
		repo := fmt.Sprintf("base/os-%d", i)
		r.Push(repo+":latest", bases[i])
		syn.RepoNames = append(syn.RepoNames, repo)
		syn.Tags++
		syn.Manifests++
	}

	var g errgroup.Group
	g.SetLimit(16)
	for i := range s.Repos {
		repo := fmt.Sprintf("team-%d/app-%d", i%10, i)
		syn.RepoNames = append(syn.RepoNames, repo)
		multi := s.MultiArchEvery > 0 && i%s.MultiArchEvery == 0
		for j := range s.Tags {
			base := bases[(i+j)%len(bases)]
			created := epoch.Add(time.Duration(i*s.Tags+j) * time.Hour)
			cfg := &v1.Config{Labels: map[string]string{"team": fmt.Sprintf("team-%d", i%10)}}
			amd := r.layered(base, fmt.Sprintf("%s-%d-amd64", repo, j), created, cfg, fmt.Sprintf("RUN build %s %d", repo, j))
			var arm v1.Image
			if multi {
				arm = r.platform(r.layered(base, fmt.Sprintf("%s-%d-arm64", repo, j), created, cfg, fmt.Sprintf("RUN build %s %d arm", repo, j)), "linux", "arm64", "v8")
			}
			syn.Tags++
			syn.Manifests++
			if multi {
				syn.Manifests++
				syn.Indexes++
			}
			ref := fmt.Sprintf("%s:v%d", repo, j)
			target := r.Ref(ref)
			// Pushes run off the test goroutine, so they return errors
			// rather than calling t.Fatal.
			g.Go(func() error {
				if !multi {
					return remote.Write(target, amd)
				}
				return remote.WriteIndex(target, mutate.AppendManifests(empty.Index,
					mutate.IndexAddendum{Add: amd, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}},
					mutate.IndexAddendum{Add: arm, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}}},
				))
			})
		}
	}
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
	return syn
}
