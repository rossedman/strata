package service

import (
	"strings"
	"testing"
	"time"

	"github.com/rossedman/strata/internal/fixture"
	"github.com/rossedman/strata/internal/model"
	"github.com/rossedman/strata/internal/store"
)

// cleanupEnv indexes images where some are abandoned: untagged, unpulled
// leaves, plus an untagged base that a tagged app is built on and an
// index with one tagged and one untagged platform.
func cleanupEnv(t *testing.T) (*Service, map[string]*model.Image) {
	t.Helper()
	sized := func(img *model.Image, sizes ...int64) *model.Image {
		for i := range img.Layers {
			img.Layers[i].Size = sizes[i]
		}
		img.Size = 0
		for _, sz := range sizes {
			img.Size += sz
		}
		return img
	}
	imgs := map[string]*model.Image{
		// old-a and old-b share a big layer nothing kept uses; old-a
		// also shares the base layer with everything.
		"base":  sized(fixture.Image("base", "reg/base/os", "os"), 100),
		"old-a": sized(fixture.Image("old-a", "reg/app/a", "os", "big", "a1"), 100, 1000, 10),
		"old-b": sized(fixture.Image("old-b", "reg/app/b", "os", "big", "b1"), 100, 1000, 20),
		"cur-a": sized(fixture.Image("cur-a", "reg/app/a", "os", "a2"), 100, 30),
		// mid is untagged but cur-c is built on it, so it is held.
		"mid":   sized(fixture.Image("mid", "reg/app/c", "os", "mid"), 100, 500),
		"cur-c": sized(fixture.Image("cur-c", "reg/app/c", "os", "mid", "c2"), 100, 500, 40),
		// arm is untagged under an untagged index whose amd platform was
		// pulled recently, so the index is held.
		"arm": sized(fixture.Image("arm", "reg/app/m", "os", "arm"), 100, 50),
		"amd": sized(fixture.Image("amd", "reg/app/m", "os", "amd"), 100, 60),
	}
	idx := fixture.Digest("index:m")
	imgs["arm"].IndexDigest, imgs["amd"].IndexDigest = idx, idx
	now := fixture.Epoch.AddDate(0, 0, 200)
	b := &store.Batch{
		Tags: []model.Tag{fixture.Tag(imgs["base"], "v1", 0), fixture.Tag(imgs["cur-a"], "v2", 0), fixture.Tag(imgs["cur-c"], "v2", 0)},
		Index: []model.IndexEntry{
			{IndexDigest: idx, Digest: imgs["arm"].Digest, Platform: model.Platform{OS: "linux", Arch: "arm64"}},
			{IndexDigest: idx, Digest: imgs["amd"].Digest, Platform: model.Platform{OS: "linux", Arch: "amd64"}},
		},
		Pushes: []model.Push{
			{Repo: "reg/app/a", Digest: imgs["old-a"].Digest, PushedAt: fixture.Epoch},
			{Repo: "reg/app/b", Digest: imgs["old-b"].Digest, PushedAt: fixture.Epoch},
			{Repo: "reg/app/c", Digest: imgs["mid"].Digest, PushedAt: fixture.Epoch},
			{Repo: "reg/app/m", Digest: imgs["amd"].Digest, PushedAt: fixture.Epoch, LastPulled: now.AddDate(0, 0, -1)},
			{Repo: "reg/app/m", Digest: imgs["arm"].Digest, PushedAt: fixture.Epoch},
		},
	}
	for _, img := range imgs {
		b.Images = append(b.Images, img)
	}
	svc := New(fixture.Open(t, b))
	svc.Now = func() time.Time { return now }
	if err := svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	return svc, imgs
}

func TestCleanup(t *testing.T) {
	t.Parallel()
	svc, imgs := cleanupEnv(t)
	c, err := svc.Cleanup(ctx, CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// old-a and old-b are candidates. arm is abandoned but held for the
	// amd platform in its index; mid is not abandoned, since cur-c is
	// built on it.
	if c.Total != 2 || c.Images != 2 || c.Held != 1 || c.Where != DefaultCleanupFilter || c.Scope != ScopeRegistry {
		t.Fatalf("cleanup = %+v", c)
	}
	// Deleting both frees the big layer once and their own tops; the
	// base layer is kept. Each alone frees only its top layer.
	if c.Size != 2*1110+10 || c.Reclaimable != 1000+10+20 {
		t.Fatalf("size = %d, reclaimable = %d", c.Size, c.Reclaimable)
	}
	if c.Candidates[0].Digest != imgs["old-b"].Digest || c.Candidates[0].Exclusive != 20 || c.Candidates[1].Exclusive != 10 {
		t.Fatalf("candidates = %+v", c.Candidates)
	}
	if cc := c.Candidates[0]; cc.Repo != "reg/app/b" || cc.Images != 1 || cc.Verdict != "abandoned" || !strings.Contains(cc.Reason, "never pulled") || cc.PushedDays == nil || *cc.PushedDays <= 0 {
		t.Fatalf("candidate = %+v", cc)
	}
	if row := c.Candidates[0].Row(); len(row) != len(CleanupColumns) {
		t.Fatalf("row = %v", row)
	}
	// By repo, the big layer is shared across repos, so neither repo
	// alone frees it.
	if len(c.Repos) != 2 || c.Repos[0] != (CleanupRepo{Repo: "reg/app/b", Candidates: 1, Size: 1120, Reclaimable: 20}) {
		t.Fatalf("repos = %+v", c.Repos)
	}

	// Per repo, the base layer counts as freed in reg/app/b, which keeps
	// nothing, but not in reg/app/a, where cur-a still uses it.
	r, err := svc.Cleanup(ctx, CleanupOptions{Scope: ScopeRepo, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if r.Reclaimable != 1000+10+1000+20+100 || len(r.Candidates) != 1 || r.Candidates[0].Exclusive != 1120 {
		t.Fatalf("repo scope = %+v", r)
	}

	// A filter picks other candidates: the whole index only when all its
	// platforms match.
	m, err := svc.Cleanup(ctx, CleanupOptions{Where: `repo = "reg/app/m"`})
	if err != nil || m.Total != 1 || m.Images != 2 || m.Candidates[0].Digest != fixture.Digest("index:m") || m.Candidates[0].Images != 2 {
		t.Fatalf("index = %+v, %v", m, err)
	}
	if m, err := svc.Cleanup(ctx, CleanupOptions{Where: `digest = "` + imgs["arm"].Digest + `"`}); err != nil || m.Total != 0 || m.Held != 1 {
		t.Fatalf("half an index = %+v, %v", m, err)
	}
	// Deleting a base everything is built on is held entirely.
	if m, err := svc.Cleanup(ctx, CleanupOptions{Where: `repo = "reg/base/os"`}); err != nil || m.Total != 0 || m.Held != 1 {
		t.Fatalf("base = %+v, %v", m, err)
	}
	for _, bad := range []CleanupOptions{{Where: "nope = 1"}, {Scope: "galaxy"}} {
		if _, err := svc.Cleanup(ctx, bad); !IsInputError(err) {
			t.Errorf("Cleanup(%+v) = %v", bad, err)
		}
	}
}

func TestLifecycles(t *testing.T) {
	t.Parallel()
	svc, imgs := cleanupEnv(t)
	lcs, err := svc.Lifecycles(ctx, LifecycleOptions{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Lifecycle{}
	for _, lc := range lcs {
		by[lc.Repo] = lc
	}
	if len(lcs) != 5 {
		t.Fatalf("repos = %d", len(lcs))
	}
	// reg/app/a and b: the old images are abandoned, so the rule agrees.
	a := by["reg/app/a"]
	if a.Name != "app/a" || a.Days != 90 || a.Expires != 1 || a.Agree != 1 || a.Policy == nil || a.Skipped != "" {
		t.Fatalf("a = %+v", a)
	}
	want := `{"rules":[{"rulePriority":1,"description":"strata: expire untagged images pushed more than 90 days ago","selection":{"tagStatus":"untagged","countType":"sinceImagePushed","countUnit":"days","countNumber":90},"action":{"type":"expire"}}]}`
	if a.Policy.Text() != want {
		t.Fatalf("policy = %s", a.Policy.Text())
	}
	// reg/app/c: mid has cur-c built on it.
	c := by["reg/app/c"]
	if c.Policy != nil || c.Expires != 1 || len(c.Conflicts) != 1 || c.Conflicts[0].Digest != imgs["mid"].Digest || !strings.Contains(c.Conflicts[0].Reason, "built on it") {
		t.Fatalf("c = %+v", c)
	}
	// reg/app/m: amd was pulled yesterday and arm sits in an index that
	// is kept for it.
	m := by["reg/app/m"]
	if m.Policy != nil || m.Expires != 2 || len(m.Conflicts) != 2 || !strings.Contains(m.Skipped, "2 image(s)") {
		t.Fatalf("m = %+v", m)
	}
	// Asking to include conflicts, with a shorter window and a repo glob.
	lcs, err = svc.Lifecycles(ctx, LifecycleOptions{Repos: []string{"reg/app/*"}, Days: 300, IncludeConflicts: true})
	if err != nil || len(lcs) != 4 || lcs[0].Expires != 0 || lcs[0].Policy == nil || lcs[0].Policy.Rules[0].Selection.CountNumber != 300 {
		t.Fatalf("filtered = %+v, %v", lcs, err)
	}
	for _, bad := range []LifecycleOptions{{Repos: []string{"["}}, {Days: -1}} {
		if _, err := svc.Lifecycles(ctx, bad); !IsInputError(err) {
			t.Errorf("Lifecycles(%+v) = %v", bad, err)
		}
	}
}
