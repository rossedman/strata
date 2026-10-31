package crawl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/rossedman/strata/internal/fixture"
	"github.com/rossedman/strata/internal/store"
	"github.com/rossedman/strata/internal/testreg"
)

// benchShape is the registry the crawl benchmarks run against: 40 repos
// of 10 images, every fourth repo multi-platform, on three bases.
var benchShape = testreg.Shape{Repos: 40, Tags: 10, MultiArchEvery: 4, Bases: 3}

// latency delays every request, so benchmarks measure what a crawl
// spends waiting on a remote registry rather than on loopback.
type latency struct {
	base  http.RoundTripper
	delay time.Duration
}

// RoundTrip waits, then forwards the request.
func (l latency) RoundTrip(r *http.Request) (*http.Response, error) {
	time.Sleep(l.delay)
	return l.base.RoundTrip(r)
}

// synthetic is shared by the benchmarks in one run; building it takes
// longer than crawling it.
var synthetic = struct {
	once sync.Once
	reg  *testreg.Synthetic
}{}

// benchRegistry returns the shared synthetic registry.
func benchRegistry(b *testing.B) *testreg.Synthetic {
	b.Helper()
	synthetic.once.Do(func() {
		// The registry must outlive the benchmark that built it, so it
		// gets its own cleanup scope.
		synthetic.reg = testreg.StartSynthetic(&keepAlive{TB: b}, benchShape)
	})
	return synthetic.reg
}

// keepAlive is a testing.TB whose cleanups never run, so a registry
// built inside one benchmark serves the rest of the run.
type keepAlive struct{ testing.TB }

// Cleanup drops the function; the process exit stops the server.
func (k *keepAlive) Cleanup(func()) {}

// benchOptions returns quiet crawl options for the synthetic registry.
func benchOptions(r *testreg.Synthetic, workers int, delay time.Duration) Options {
	return Options{
		Registry:  r.Host,
		Insecure:  true,
		Workers:   workers,
		Keychain:  authn.NewMultiKeychain(),
		Transport: latency{base: http.DefaultTransport, delay: delay},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// report adds the crawl's per-image and per-tag request counts.
func report(b *testing.B, stats Stats, tags int) {
	b.ReportMetric(stats.RequestsPerImage(), "requests/image")
	b.ReportMetric(float64(stats.Requests)/float64(tags), "requests/tag")
}

// BenchmarkCrawlFirst crawls the registry into an empty index with 2 ms
// per request, at several worker counts.
func BenchmarkCrawlFirst(b *testing.B) {
	r := benchRegistry(b)
	for _, workers := range []int{4, 16, 64} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			var stats Stats
			for b.Loop() {
				b.StopTimer()
				s := fixture.Open(b, nil)
				b.StartTimer()
				var err error
				if stats, err = Crawl(context.Background(), s, benchOptions(r, workers, 2*time.Millisecond)); err != nil {
					b.Fatal(err)
				}
			}
			if int(stats.Images) != r.Manifests {
				b.Fatalf("indexed %d images, want %d", stats.Images, r.Manifests)
			}
			report(b, stats, r.Tags)
			b.ReportMetric(float64(r.Manifests)*float64(b.N)/b.Elapsed().Seconds(), "images/s")
		})
	}
}

// BenchmarkRecrawl re-crawls an index that already holds every image,
// which is what a scheduled re-index does.
func BenchmarkRecrawl(b *testing.B) {
	r := benchRegistry(b)
	s := fixture.Open(b, nil)
	if _, err := Crawl(context.Background(), s, benchOptions(r, 16, 0)); err != nil {
		b.Fatal(err)
	}
	var stats Stats
	for b.Loop() {
		var err error
		if stats, err = Crawl(context.Background(), s, benchOptions(r, 16, 2*time.Millisecond)); err != nil {
			b.Fatal(err)
		}
	}
	if stats.Images != 0 {
		b.Fatalf("re-crawl found %d new images", stats.Images)
	}
	b.ReportMetric(float64(stats.Requests)/float64(r.Tags), "requests/tag")
}

// BenchmarkCrawlLister crawls through a registry API listing, as for
// ECR, first into an empty index and then again.
func BenchmarkCrawlLister(b *testing.B) {
	r := benchRegistry(b)
	lister := listing(b, r)
	b.Run("first", func(b *testing.B) {
		var stats Stats
		for b.Loop() {
			b.StopTimer()
			s := fixture.Open(b, nil)
			b.StartTimer()
			o := benchOptions(r, 16, 2*time.Millisecond)
			o.Lister = lister
			var err error
			if stats, err = Crawl(context.Background(), s, o); err != nil {
				b.Fatal(err)
			}
		}
		report(b, stats, r.Tags)
	})
	b.Run("recrawl", func(b *testing.B) {
		s := fixture.Open(b, nil)
		o := benchOptions(r, 16, 0)
		o.Lister = lister
		if _, err := Crawl(context.Background(), s, o); err != nil {
			b.Fatal(err)
		}
		var stats Stats
		for b.Loop() {
			var err error
			if stats, err = Crawl(context.Background(), s, o); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(stats.Requests), "requests")
	})
}

// staticLister serves a listing built once and fetches manifests in
// batches, as the ECR API does. Its calls are counted as API calls; the
// HTTP requests it makes to do so go around the crawl's transport, so they
// are not counted twice.
type staticLister struct {
	host   string
	repos  []string
	images map[string][]Listed
	calls  atomic.Int64
}

// Repos returns the listed repos.
func (l *staticLister) Repos(context.Context) ([]string, error) {
	l.calls.Add(1)
	return l.repos, nil
}

// Images returns a repo's listed images.
func (l *staticLister) Images(_ context.Context, repo string) ([]Listed, error) {
	l.calls.Add(1)
	return l.images[repo], nil
}

// Manifests fetches manifests by digest.
func (l *staticLister) Manifests(_ context.Context, repo string, digests []string) (map[string][]byte, error) {
	l.calls.Add(1)
	out := map[string][]byte{}
	for _, d := range digests {
		ref, err := name.NewDigest(l.host+"/"+repo+"@"+d, name.Insecure)
		if err != nil {
			return nil, err
		}
		desc, err := remote.Get(ref)
		if err != nil {
			return nil, err
		}
		out[d] = desc.Manifest
	}
	return out, nil
}

// Calls counts the API calls made.
func (l *staticLister) Calls() int64 { return l.calls.Load() }

// listing builds a static listing of the synthetic registry.
func listing(tb testing.TB, r *testreg.Synthetic) *staticLister {
	tb.Helper()
	l := &staticLister{host: r.Host, repos: r.RepoNames, images: map[string][]Listed{}}
	for _, repo := range r.RepoNames {
		ref, err := name.NewRepository(r.Host+"/"+repo, name.Insecure)
		if err != nil {
			tb.Fatal(err)
		}
		tags, err := remote.List(ref)
		if err != nil {
			tb.Fatal(err)
		}
		for _, tag := range tags {
			desc, err := remote.Head(ref.Tag(tag))
			if err != nil {
				tb.Fatal(err)
			}
			l.images[repo] = append(l.images[repo], Listed{Digest: desc.Digest.String(), MediaType: string(desc.MediaType), Tags: []string{tag}, PushedAt: time.Now()})
		}
	}
	return l
}

// TestRequestBudget pins how many requests of each kind a crawl makes,
// so a change that adds requests fails here rather than in production.
// Handshakes are the one count that varies, with how many workers start
// on a repo at once.
func TestRequestBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := testreg.StartSynthetic(t, testreg.Shape{Repos: 8, Tags: 5, MultiArchEvery: 4, Bases: 2})
	repos := len(r.RepoNames)
	crawl := func(s *store.Store, lister Lister) map[string]int {
		t.Helper()
		pc := &pathCounter{base: http.DefaultTransport, n: map[string]int{}}
		o := benchOptions(r, 4, 0)
		o.Transport = pc
		o.Lister = lister
		if _, err := Crawl(ctx, s, o); err != nil {
			t.Fatal(err)
		}
		if limit := 1 + repos*o.Workers; pc.n["ping"] > limit {
			t.Errorf("handshakes = %d, want at most %d", pc.n["ping"], limit)
		}
		delete(pc.n, "ping")
		return pc.n
	}
	check := func(name string, got map[string]int, want map[string]int) {
		t.Helper()
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %d, want %d (all: %v)", name, k, got[k], v, got)
			}
		}
		for k, v := range got {
			if _, ok := want[k]; !ok && v > 0 {
				t.Errorf("%s: unexpected %d %s requests", name, v, k)
			}
		}
	}
	tagLists := map[string]int{}
	for _, repo := range r.RepoNames {
		tagLists["GET /v2/"+repo+"/tags/list"] = 1
	}
	fetches := map[string]int{"GET manifest": r.Indexes + r.Manifests, "blob": r.Manifests}

	s := fixture.Open(t, nil)
	first := map[string]int{"GET /v2/_catalog": 1, "HEAD manifest": r.Tags}
	maps.Copy(first, tagLists)
	maps.Copy(first, fetches)
	check("first crawl", crawl(s, nil), first)

	again := map[string]int{"GET /v2/_catalog": 1, "HEAD manifest": r.Tags}
	maps.Copy(again, tagLists)
	check("re-crawl", crawl(s, nil), again)

	// Through a listing that fetches manifests in batches, as for ECR, the
	// only per-image request left is the config.
	l := listing(t, r)
	before := l.Calls()
	check("first crawl with a listing", crawl(fixture.Open(t, nil), l), map[string]int{"blob": r.Manifests})
	// One call to list repos, one to list each repo, one batch of tagged
	// manifests per repo, and one batch of platform images per repo that
	// holds multi-platform indexes.
	multi := (8 + 3) / 4
	if got, want := l.Calls()-before, int64(1+repos+repos+multi); got != want {
		t.Errorf("first crawl with a listing: %d API calls, want %d", got, want)
	}
	before = l.Calls()
	check("re-crawl with a listing", crawl(s, l), map[string]int{})
	if got, want := l.Calls()-before, int64(1+repos); got != want {
		t.Errorf("re-crawl with a listing: %d API calls, want %d", got, want)
	}
}

// partialFetcher returns only every other manifest asked for, or fails.
type partialFetcher struct {
	*staticLister
	fail bool
}

// Manifests drops half of what the listing would return.
func (p *partialFetcher) Manifests(ctx context.Context, repo string, digests []string) (map[string][]byte, error) {
	if p.fail {
		return nil, errors.New("throttled")
	}
	got, err := p.staticLister.Manifests(ctx, repo, digests)
	for i, d := range digests {
		if i%2 == 1 {
			delete(got, d)
		}
	}
	return got, err
}

func TestBatchedManifestsFallBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := testreg.StartSynthetic(t, testreg.Shape{Repos: 6, Tags: 4, MultiArchEvery: 3, Bases: 2})
	pc := &pathCounter{base: http.DefaultTransport, n: map[string]int{}}
	o := benchOptions(r, 4, 0)
	o.Transport = pc
	o.Lister = &partialFetcher{staticLister: listing(t, r)}
	stats, err := Crawl(ctx, fixture.Open(t, nil), o)
	if err != nil {
		t.Fatal(err)
	}
	// Every image is indexed; the manifests the batch left out were
	// fetched one by one.
	if int(stats.Images) != r.Manifests || pc.n["GET manifest"] == 0 || pc.n["GET manifest"] >= r.Indexes+r.Manifests {
		t.Fatalf("images = %d of %d, manifest requests = %d", stats.Images, r.Manifests, pc.n["GET manifest"])
	}
	o.Lister = &partialFetcher{staticLister: listing(t, r), fail: true}
	if _, err := Crawl(ctx, fixture.Open(t, nil), o); err == nil || !strings.Contains(err.Error(), "fetch manifests: throttled") {
		t.Fatalf("err = %v", err)
	}
}
