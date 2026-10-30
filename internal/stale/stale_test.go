package stale

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rossedman/strata/internal/fixture"
	"github.com/rossedman/strata/internal/lineage"
	"github.com/rossedman/strata/internal/model"
	"github.com/rossedman/strata/internal/store"
)

var now = fixture.Epoch.AddDate(0, 0, 100)

// evaluator returns an evaluator over events with the default policy.
func evaluator(events ...model.TagEvent) *Evaluator {
	return &Evaluator{Policy: DefaultPolicy(), History: NewHistory(events), Repos: RepoImages{}, Now: now}
}

func TestEvaluate(t *testing.T) {
	t.Parallel()
	const base = "reg.io/base/go"
	p1, p2, p3, p4 := fixture.Digest("p1"), fixture.Digest("p2"), fixture.Digest("p3"), fixture.Digest("p4")
	self, other := fixture.Digest("self"), fixture.Digest("other")
	parent := &ParentFacts{Digest: p1, Repo: base, Created: fixture.Epoch}
	history := []model.TagEvent{
		fixture.Event(base, "1.22", p1, 0),
		fixture.Event(base, "1.22", p2, 50),
		fixture.Event(base, "1.22", p2, 55), // a re-crawl seeing the same digest is not a move
		fixture.Event(base, "1.22", p3, 59),
		fixture.Event(base, "1.22", p4, 60),
		fixture.Event(base, "latest", p1, 0),
		fixture.Event(base, "latest", p2, 90),
	}
	tests := []struct {
		name    string
		ev      *Evaluator
		facts   Facts
		verdict string
		reason  string
	}{
		{
			name:    "on the current base and tagged",
			ev:      evaluator(fixture.Event(base, "1.22", p1, 0)),
			facts:   Facts{Digest: self, Tags: 1, Parent: parent},
			verdict: Fresh,
		},
		{
			name:    "drift picks the tag that moved least",
			ev:      evaluator(history...),
			facts:   Facts{Digest: self, Tags: 1, Parent: parent},
			verdict: Drifting,
			reason:  "base reg.io/base/go:latest moved 1 time since this image's parent (last move 10 days ago)",
		},
		{
			name:    "three generations behind",
			ev:      evaluator(history[:5]...),
			facts:   Facts{Digest: self, Tags: 1, Parent: parent},
			verdict: Stale,
			reason:  "base reg.io/base/go:1.22 moved 3 times since this image's parent (last move 40 days ago)",
		},
		{
			name: "superseded recently",
			ev: evaluator(fixture.Event("reg.io/app", "v1", self, 0),
				fixture.Event("reg.io/app", "v1", other, 90)),
			facts:   Facts{Digest: self, Tags: 1},
			verdict: Drifting,
			reason:  "superseded under reg.io/app:v1 10 days ago",
		},
		{
			name: "superseded long ago",
			ev: evaluator(fixture.Event("reg.io/app", "v1", self, 0),
				fixture.Event("reg.io/app", "v1", other, 10)),
			facts:   Facts{Digest: self, Tags: 1},
			verdict: Stale,
		},
		{
			name: "deleted tags are ignored",
			ev: evaluator(fixture.Event("reg.io/app", "v1", self, 0),
				model.TagEvent{Repo: "reg.io/app", Tag: "v1", SeenAt: fixture.Epoch.AddDate(0, 0, 5)}),
			facts:   Facts{Digest: self, Tags: 1},
			verdict: Fresh,
		},
		{
			name:    "untagged leaf with no pull data",
			ev:      evaluator(),
			facts:   Facts{Digest: self},
			verdict: Abandoned,
			reason:  "untagged and no descendants (no pull data)",
		},
		{
			name:    "untagged base with children stays fresh",
			ev:      evaluator(),
			facts:   Facts{Digest: self, Descendants: 3},
			verdict: Fresh,
			reason:  "untagged, but other images are built on it",
		},
		{
			name:    "untagged leaf pulled recently",
			ev:      &Evaluator{Policy: DefaultPolicy(), History: History{}, Now: now, HavePulls: true},
			facts:   Facts{Digest: self, LastPulled: now.AddDate(0, 0, -3)},
			verdict: Fresh,
		},
		{
			name:    "untagged leaf not pulled in 120 days",
			ev:      &Evaluator{Policy: DefaultPolicy(), History: History{}, Now: now, HavePulls: true},
			facts:   Facts{Digest: self, LastPulled: now.AddDate(0, 0, -120)},
			verdict: Abandoned,
			reason:  "untagged, no descendants and no pulls in 90 days",
		},
		{
			name:    "phantom parent says nothing about drift",
			ev:      evaluator(),
			facts:   Facts{Digest: self, Tags: 1, Parent: &ParentFacts{Digest: "phantom:x", Phantom: true}},
			verdict: Fresh,
		},
		{
			name:    "no history, newer image in the base repo",
			ev:      &Evaluator{Policy: DefaultPolicy(), History: History{}, Now: now, Repos: RepoImages{base: {fixture.Epoch.AddDate(0, 0, 20)}}},
			facts:   Facts{Digest: self, Tags: 1, Parent: parent},
			verdict: Drifting,
			reason:  "has 1 newer image(s) than this image's parent (inferred, no tag history)",
		},
		{
			name:    "no history and no creation date",
			ev:      evaluator(),
			facts:   Facts{Digest: self, Tags: 1, Parent: &ParentFacts{Digest: p1, Repo: base}},
			verdict: Fresh,
		},
		{
			name:    "EOL root",
			ev:      &Evaluator{Policy: Policy{Thresholds: DefaultPolicy().Thresholds, EOLRoots: []string{"docker.io/library/debian:buster*"}}, History: History{}, Now: now},
			facts:   Facts{Digest: self, Tags: 1, RootRefs: []string{"docker.io/library/debian:buster-slim"}},
			verdict: Stale,
			reason:  "root base docker.io/library/debian:buster-slim is past end of life",
		},
		{
			name:    "deprecated label",
			ev:      evaluator(),
			facts:   Facts{Digest: self, Tags: 1, Labels: map[string]string{"deprecated": "true"}},
			verdict: Stale,
			reason:  "label deprecated=true",
		},
		{
			name:    "expired label",
			ev:      evaluator(),
			facts:   Facts{Digest: self, Tags: 1, Labels: map[string]string{"expires": "2026-02-01"}},
			verdict: Stale,
			reason:  "label expires=2026-02-01 is in the past",
		},
		{
			name:    "future expiry is fine",
			ev:      evaluator(),
			facts:   Facts{Digest: self, Tags: 1, Labels: map[string]string{"expires": "2030-01-01T00:00:00Z"}, Created: fixture.Epoch},
			verdict: Fresh,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := tt.ev.Evaluate(tt.facts)
			if r.Verdict != tt.verdict {
				t.Fatalf("verdict = %s, want %s (reasons %v)", r.Verdict, tt.verdict, r.Reasons)
			}
			if tt.reason != "" && !strings.Contains(strings.Join(r.Reasons, "\n"), tt.reason) {
				t.Fatalf("reasons %q lack %q", r.Reasons, tt.reason)
			}
		})
	}
}

func TestOverrides(t *testing.T) {
	t.Parallel()
	p, err := ParsePolicy([]byte(`
thresholds: {superseded_days: 30}
overrides:
  - repo: "reg.io/sandbox/*"
    labels: {env: dev}
    thresholds: {superseded_days: 7, stale_generations: 5, abandoned_pull_days: 10}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.For("reg.io/sandbox/x", map[string]string{"env": "dev"}); got.SupersededDays != 7 || got.StaleGenerations != 5 || got.AbandonedPullDays != 10 {
		t.Fatalf("override not applied: %+v", got)
	}
	if got := p.For("reg.io/sandbox/x", map[string]string{"env": "prod"}); got.SupersededDays != 30 {
		t.Fatalf("override applied to the wrong label: %+v", got)
	}
	if got := p.For("reg.io/prod/x", map[string]string{"env": "dev"}); got.SupersededDays != 30 {
		t.Fatalf("override applied to the wrong repo: %+v", got)
	}
}

func TestParsePolicyErrors(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"thresholds: {stale_generations: 0}",
		"unknown_key: 1",
		"eol_roots: ['[']",
		"thresholds: [",
	} {
		if _, err := ParsePolicy([]byte(in)); err == nil {
			t.Errorf("ParsePolicy(%q) succeeded", in)
		}
	}
	if p, err := ParsePolicy(nil); err != nil || p.Thresholds.SupersededDays != 30 {
		t.Fatalf("empty policy: %+v %v", p, err)
	}
	if _, err := LoadPolicy("/does/not/exist.yaml"); err == nil {
		t.Fatal("LoadPolicy read a missing file")
	}
	if p, err := LoadPolicy(""); err != nil || p.Thresholds.StaleGenerations != 2 {
		t.Fatalf("LoadPolicy(\"\") = %+v %v", p, err)
	}
}

func TestRecompute(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base1 := fixture.Image("go-1", "reg.io/base/go", "d1", "g1")
	base2 := fixture.Created(fixture.Image("go-2", "reg.io/base/go", "d1", "g2"), 10)
	base3 := fixture.Created(fixture.Image("go-3", "reg.io/base/go", "d1", "g3"), 20)
	app := fixture.Label(fixture.Image("app", "reg.io/team/app", "d1", "g1", "a1"), "team", "payments")
	orphan := fixture.Image("orphan", "reg.io/team/old", "o1")
	b := &store.Batch{
		Images: []*model.Image{base1, base2, base3, app, orphan},
		Tags:   []model.Tag{fixture.Tag(base3, "1.22", 20), fixture.Tag(app, "v1", 1)},
		Events: []model.TagEvent{
			fixture.Event("reg.io/base/go", "1.22", base1.Digest, 0),
			fixture.Event("reg.io/base/go", "1.22", base2.Digest, 10),
			fixture.Event("reg.io/base/go", "1.22", base3.Digest, 20),
		},
		Referrers: []model.Referrer{{Subject: app.Digest, Digest: fixture.Digest("sbom"), ArtifactType: "application/spdx+json", Kind: model.KindSBOM}},
	}
	s := fixture.Open(t, b)
	if _, err := lineage.Recompute(ctx, s.DB(), lineage.Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Recompute(ctx, s.DB(), DefaultPolicy(), now); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	rows, err := s.DB().Query(`SELECT digest, verdict, reasons::JSON::VARCHAR FROM staleness`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var d, v, reasons string
		if err := rows.Scan(&d, &v, &reasons); err != nil {
			t.Fatal(err)
		}
		got[d] = v
		if d == app.Digest && !strings.Contains(reasons, "moved 2 times") {
			t.Fatalf("app reasons = %s", reasons)
		}
	}
	want := map[string]string{
		app.Digest: Stale, orphan.Digest: Abandoned, base3.Digest: Fresh,
		base1.Digest: Stale, // superseded under 1.22 90 days ago
	}
	for d, v := range want {
		if got[d] != v {
			t.Errorf("%s: verdict %s, want %s", d, got[d], v)
		}
	}
	if _, err := Recompute(ctx, s.DB(), DefaultPolicy(), time.Now()); err != nil {
		t.Fatalf("second recompute: %v", err)
	}
}

func TestRecomputeErrors(t *testing.T) {
	t.Parallel()
	s := fixture.Open(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Recompute(ctx, s.DB(), DefaultPolicy(), now); err == nil {
		t.Fatal("Recompute ran on a canceled context")
	}
}

func BenchmarkEvaluate(b *testing.B) {
	var events []model.TagEvent
	for i := range 200 {
		events = append(events, fixture.Event("reg.io/base/go", "1.22", fixture.Digest(string(rune('a'+i%26))+string(rune(i))), i))
	}
	ev := evaluator(events...)
	f := Facts{Digest: fixture.Digest("x"), Tags: 1, Parent: &ParentFacts{Digest: events[3].Digest, Repo: "reg.io/base/go"}}
	b.ReportAllocs()
	for b.Loop() {
		ev.Evaluate(f)
	}
}

// TestWriteLargeIndex writes more verdicts than fit in one DuckDB row
// group, where staging inside a transaction loses them (see store.Stage).
func TestWriteLargeIndex(t *testing.T) {
	t.Parallel()
	s := fixture.Open(t, nil)
	const n = 130_000
	results := make([]Result, n)
	for i := range results {
		results[i] = Result{Digest: fmt.Sprintf("sha256:%064d", i), Verdict: "fresh", Reasons: []string{}}
	}
	changed, err := write(context.Background(), s.DB(), results, time.Now())
	if err != nil || len(changed) != n {
		t.Fatalf("first write changed %d, %v", len(changed), err)
	}
	results[5].Verdict = "stale"
	if changed, err = write(context.Background(), s.DB(), results, time.Now()); err != nil || len(changed) != 1 {
		t.Fatalf("second write changed %d, %v", len(changed), err)
	}
	var stored, stale int
	if err := s.DB().QueryRow(`SELECT count(*), count(*) FILTER (WHERE verdict = 'stale') FROM staleness`).Scan(&stored, &stale); err != nil {
		t.Fatal(err)
	}
	if stored != n || stale != 1 {
		t.Fatalf("stored %d verdicts, %d stale", stored, stale)
	}
}
