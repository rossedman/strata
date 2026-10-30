package crawl

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rossedman/strata/internal/fixture"
	"github.com/rossedman/strata/internal/model"
	"github.com/rossedman/strata/internal/testreg"
)

func TestGlob(t *testing.T) {
	t.Parallel()
	tests := []struct {
		glob string
		yes  []string
		no   []string
	}{
		{"payments/*", []string{"payments/api", "payments/worker"}, []string{"payments/api/v2", "base/go", "payments"}},
		{"payments/**", []string{"payments/api", "payments/api/v2"}, []string{"base/go"}},
		{"**/api", []string{"api", "payments/api", "a/b/api"}, []string{"payments/apis"}},
		{"a/**/b", []string{"a/b", "a/x/b", "a/x/y/b"}, []string{"a/xb"}},
		{"team-?", []string{"team-a"}, []string{"team-ab", "team-/"}},
		{"v[0-9]*", []string{"v1", "v10.2"}, []string{"vx", "latest"}},
		{"[!v]*", []string{"latest", "1.0"}, []string{"v1"}},
		{"pr-*", []string{"pr-12"}, []string{"main-pr-12"}},
		{"1.0", []string{"1.0"}, []string{"1x0"}},
		{"a+b", []string{"a+b"}, []string{"aab"}},
		{"café-*", []string{"café-1"}, []string{"cafe-1"}},
	}
	for _, tt := range tests {
		re, err := Glob(tt.glob)
		if err != nil {
			t.Fatalf("Glob(%q): %v", tt.glob, err)
		}
		for _, s := range tt.yes {
			if !re.MatchString(s) {
				t.Errorf("%q should match %q", tt.glob, s)
			}
		}
		for _, s := range tt.no {
			if re.MatchString(s) {
				t.Errorf("%q should not match %q", tt.glob, s)
			}
		}
	}
	for _, bad := range []string{"", "v[0-9"} {
		if _, err := Glob(bad); err == nil {
			t.Errorf("Glob(%q) should fail", bad)
		}
	}
}

func TestParsePlatform(t *testing.T) {
	t.Parallel()
	p, err := ParsePlatform("linux/arm64/v8")
	if err != nil || p != (model.Platform{OS: "linux", Arch: "arm64", Variant: "v8"}) {
		t.Fatalf("got %+v, %v", p, err)
	}
	for _, bad := range []string{"linux", "linux/", "/amd64", "a/b/c/d"} {
		if _, err := ParsePlatform(bad); err == nil {
			t.Errorf("ParsePlatform(%q) should fail", bad)
		}
	}
}

func TestFilterMatch(t *testing.T) {
	t.Parallel()
	m, err := Filter{
		IncludeRepos: []string{"payments/**"}, ExcludeRepos: []string{"*/sandbox"},
		ExcludeTags: []string{"pr-*"}, Platforms: []string{"linux/amd64", "linux/arm64"},
		PushedSince: 24 * time.Hour,
	}.compile()
	if err != nil {
		t.Fatal(err)
	}
	if !m.repo("payments/api") || m.repo("payments/sandbox") || m.repo("base/go") {
		t.Error("repo globs")
	}
	if !m.tag("v1") || m.tag("pr-3") {
		t.Error("tag globs")
	}
	if !m.platform(model.Platform{OS: "linux", Arch: "arm64", Variant: "v8"}) || m.platform(model.Platform{OS: "linux", Arch: "s390x"}) {
		t.Error("platforms: a platform without a variant matches every variant")
	}
	now := fixture.Epoch
	if !m.pushed(now.Add(-time.Hour), now) || m.pushed(now.Add(-48*time.Hour), now) || !m.pushed(time.Time{}, now) {
		t.Error("pushed-since keeps recent and unknown push times")
	}
	if all, _ := (Filter{}).compile(); !all.repo("x") || !all.tag("x") || !all.platform(model.Platform{OS: "windows", Arch: "amd64"}) || !all.pushed(time.Time{}, now) {
		t.Error("an empty filter keeps everything")
	}
	for _, f := range []Filter{{IncludeRepos: []string{""}}, {ExcludeTags: []string{"[x"}}, {Platforms: []string{"linux"}}, {PushedSince: -time.Hour}} {
		if f.Validate() == nil {
			t.Errorf("%+v should not validate", f)
		}
	}
}

// pathCounter counts registry requests by kind.
type pathCounter struct {
	base http.RoundTripper
	mu   sync.Mutex
	n    map[string]int
}

// RoundTrip counts the request, then forwards it.
func (p *pathCounter) RoundTrip(r *http.Request) (*http.Response, error) {
	k := r.Method + " " + r.URL.Path
	switch {
	case r.URL.Path == "/v2/":
		k = "ping"
	case strings.Contains(r.URL.Path, "/manifests/"):
		k = r.Method + " manifest"
	case strings.Contains(r.URL.Path, "/blobs/"):
		k = "blob"
	case strings.Contains(r.URL.Path, "/referrers/"):
		k = "referrers"
	}
	p.mu.Lock()
	p.n[k]++
	p.mu.Unlock()
	return p.base.RoundTrip(r)
}

// counted returns options whose requests are counted by kind.
func counted(r *testreg.Registry) (Options, *pathCounter) {
	o := opts(r)
	pc := &pathCounter{base: http.DefaultTransport, n: map[string]int{}}
	o.Transport = pc
	return o, pc
}

func TestCrawlReusesConnections(t *testing.T) {
	t.Parallel()
	r := testreg.Start(t)
	o, pc := counted(r)
	if _, err := Crawl(context.Background(), fixture.Open(t, nil), o); err != nil {
		t.Fatal(err)
	}
	// At most one handshake for the catalog and one per worker per repo,
	// not one per request.
	if limit := 1 + 4*o.Workers; pc.n["ping"] > limit {
		t.Fatalf("pings = %d, want at most %d: %v", pc.n["ping"], limit, pc.n)
	}
}

func TestCrawlFilters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := testreg.Start(t)
	s := fixture.Open(t, nil)
	o, pc := counted(r)
	o.Filter = Filter{ExcludeRepos: []string{"payments/worker"}, ExcludeTags: []string{"latest"}, Platforms: []string{"linux/amd64"}}
	stats, err := Crawl(ctx, s, o)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ReposFiltered != 1 || stats.TagsFiltered != 1 || stats.ImagesFiltered != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	// debian, go amd64 and api; arm64 and worker are filtered out.
	if n := count(t, s, `SELECT count(*) FROM images`); n != 3 {
		t.Fatalf("images = %d", n)
	}
	if n := count(t, s, `SELECT count(*) FROM tags WHERE tag = 'latest'`); n != 0 {
		t.Fatal("an excluded tag was recorded")
	}
	// The worker repo was never listed and arm64's manifest never read.
	if pc.n["GET /v2/payments/worker/tags/list"] != 0 {
		t.Fatalf("filtered repo was listed: %v", pc.n)
	}

	// Crawling again without the tag filter adds latest; a tag the filter
	// skips is left as it was, not deleted.
	o.Filter = Filter{IncludeTags: []string{"latest", "12", "1.22", "v1"}}
	if _, err := Crawl(ctx, s, o); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM tags WHERE repo LIKE '%payments/api' AND tag = '1.0'`); n != 1 {
		t.Fatal("a tag skipped by the filter was deleted")
	}
	if n := count(t, s, `SELECT count(*) FROM tag_history WHERE digest IS NULL`); n != 0 {
		t.Fatal("a filtered tag was recorded as deleted")
	}
}

func TestPushedSinceNeedsLister(t *testing.T) {
	t.Parallel()
	o := opts(testreg.Start(t))
	o.Filter = Filter{PushedSince: time.Hour}
	if _, err := Crawl(context.Background(), fixture.Open(t, nil), o); err == nil || !strings.Contains(err.Error(), "push times") {
		t.Fatalf("err = %v", err)
	}
	o.Filter = Filter{Platforms: []string{"bad"}}
	if _, err := Crawl(context.Background(), fixture.Open(t, nil), o); err == nil {
		t.Fatal("a bad filter should fail the crawl")
	}
}

func FuzzGlob(f *testing.F) {
	for _, s := range []string{"payments/**", "**/api", "v[0-9]*", "[!v]*", "a?b", "[", "**/", `\`, "[]]"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, g string) {
		re, err := Glob(g)
		if err != nil {
			return
		}
		// A glob with no special characters matches exactly itself.
		if !strings.ContainsAny(g, "*?[") && !re.MatchString(g) {
			t.Fatalf("%q does not match itself", g)
		}
	})
}
