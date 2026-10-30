package crawl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/rossedman/strata/internal/fixture"
	"github.com/rossedman/strata/internal/stale"
	"github.com/rossedman/strata/internal/testreg"
)

// fakeLister lists a test registry the way a registry API would, with
// push times. It resolves tags with its own client, so the crawl's request
// counts show only what the crawl itself asked for.
type fakeLister struct {
	r      *testreg.Registry
	pushed map[string]time.Time
	extra  map[string][]Listed
	err    error
}

// Repos lists the test registry's repos.
func (f *fakeLister) Repos(context.Context) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []string{"base/debian", "base/go", "payments/api", "payments/worker"}, nil
}

// Images lists a repo's tags, grouped by the manifest they point at.
func (f *fakeLister) Images(_ context.Context, repo string) ([]Listed, error) {
	ref, err := name.NewRepository(f.r.Host+"/"+repo, name.Insecure)
	if err != nil {
		return nil, err
	}
	tags, err := remote.List(ref)
	if err != nil {
		return nil, err
	}
	byDigest := map[string]*Listed{}
	var out []*Listed
	for _, tag := range tags {
		desc, err := remote.Head(ref.Tag(tag))
		if err != nil {
			return nil, err
		}
		d := desc.Digest.String()
		if byDigest[d] == nil {
			byDigest[d] = &Listed{Digest: d, MediaType: string(desc.MediaType), PushedAt: f.pushed[repo], LastPulled: f.pushed[repo].Add(time.Hour)}
			out = append(out, byDigest[d])
		}
		byDigest[d].Tags = append(byDigest[d].Tags, tag)
	}
	res := make([]Listed, 0, len(out))
	for _, l := range out {
		res = append(res, *l)
	}
	return append(res, f.extra[repo]...), nil
}

func TestCrawlWithLister(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := testreg.Start(t)
	s := fixture.Open(t, nil)
	now := fixture.Epoch
	sbom := "sha256:" + strings.Repeat("ab", 32)
	o, pc := counted(r)
	o.Now = func() time.Time { return now }
	o.Lister = &fakeLister{r: r, pushed: map[string]time.Time{
		"base/debian":     now.AddDate(-2, 0, 0),
		"base/go":         now.AddDate(0, -1, 0),
		"payments/api":    now.AddDate(0, 0, -2),
		"payments/worker": now.AddDate(0, 0, -1),
	}, extra: map[string][]Listed{
		"payments/api": {{Digest: sbom, Subject: r.Digest("api"), ArtifactType: "application/spdx+json", PushedAt: now}},
	}}
	o.Filter = Filter{PushedSince: 365 * 24 * time.Hour}
	stats, err := Crawl(ctx, s, o)
	if err != nil {
		t.Fatal(err)
	}
	// debian was pushed two years ago, so it is skipped.
	if stats.ImagesFiltered != 1 || stats.Images != 4 {
		t.Fatalf("stats = %+v", stats)
	}
	// Tags come from the listing and artifacts with it: no HEAD per tag
	// and no referrers requests.
	if pc.n["HEAD manifest"] != 0 || pc.n["referrers"] != 0 || pc.n["GET /v2/_catalog"] != 0 {
		t.Fatalf("requests = %v", pc.n)
	}
	if n := count(t, s, `SELECT count(*) FROM image_view WHERE has_sbom`); n != 1 {
		t.Fatalf("images with an SBOM = %d", n)
	}
	if n := count(t, s, `SELECT count(*) FROM pushes`); n != 6 {
		t.Fatalf("pushes = %d", n)
	}
	if n := count(t, s, `SELECT count(*) FROM pulls`); n != 5 {
		t.Fatalf("pulls = %d", n)
	}
	// Platform images in an index take the index's push time.
	var pushedAt time.Time
	var age int
	if err := s.DB().QueryRow(`SELECT pushed, pushed_age_days FROM image_view WHERE digest = ?`, r.Digest("go-arm64")).Scan(&pushedAt, &age); err != nil {
		t.Fatal(err)
	}
	if !pushedAt.Equal(now.AddDate(0, -1, 0)) || age <= 0 {
		t.Fatalf("pushed = %s, age = %d", pushedAt, age)
	}
	if n := count(t, s, `SELECT count(*) FROM image_view WHERE first_seen IS NULL`); n != 0 {
		t.Fatal("first_seen is unset")
	}
	// Pulls of an index count for its platform images.
	if _, err := stale.Recompute(ctx, s.DB(), stale.DefaultPolicy(), now); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM image_view WHERE stale_last_pulled_days IS NOT NULL`); n != 4 {
		t.Fatalf("images with a last pull = %d", n)
	}

	// A known image stays tagged even once it falls outside the window.
	o.Filter = Filter{PushedSince: 24 * time.Hour}
	stats, err = Crawl(ctx, s, o)
	if err != nil {
		t.Fatal(err)
	}
	if stats.TagMoves != 0 {
		t.Fatalf("re-crawl moved tags: %+v", stats)
	}
}

func TestListerErrors(t *testing.T) {
	t.Parallel()
	r := testreg.Start(t)
	o := opts(r)
	o.Lister = &fakeLister{r: r, err: errors.New("denied")}
	if _, err := Crawl(context.Background(), fixture.Open(t, nil), o); err == nil || !strings.Contains(err.Error(), "list repos: denied") {
		t.Fatalf("err = %v", err)
	}
	// A seeded repo the listing cannot find fails with the repo named.
	o.Lister = &fakeLister{r: r}
	o.Repos = []string{"missing/repo"}
	if _, err := Crawl(context.Background(), fixture.Open(t, nil), o); err == nil || !strings.Contains(err.Error(), "missing/repo") {
		t.Fatalf("err = %v", err)
	}
	// A listing with a malformed digest fails the crawl.
	o.Repos = nil
	o.Lister = &fakeLister{r: r, extra: map[string][]Listed{"base/debian": {{Digest: "bogus", Tags: []string{"bad"}}}}}
	if _, err := Crawl(context.Background(), fixture.Open(t, nil), o); err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("err = %v", err)
	}
}
