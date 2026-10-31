package service

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

// derived lists every table a refresh writes, as queries whose rows are
// compared between an incremental and a full refresh. Bookkeeping columns
// that record when a row was written are left out.
var derived = map[string]string{
	"edges":         `SELECT * FROM edges`,
	"phantoms":      `SELECT * FROM phantoms`,
	"lineage":       `SELECT * FROM lineage`,
	"staleness":     `SELECT * EXCLUDE (computed_at, age_days) REPLACE (reasons::JSON::VARCHAR AS reasons) FROM staleness`,
	"subtree_stats": `SELECT * FROM subtree_stats`,
	// Lists built with list(DISTINCT ...) come back in no fixed order.
	"image_facts": `SELECT * REPLACE (labels::JSON::VARCHAR AS labels, stale_reasons::JSON::VARCHAR AS stale_reasons,
		list_sort(tag_refs) AS tag_refs, list_sort(refs) AS refs, list_sort(parent_refs) AS parent_refs, list_sort(root_refs) AS root_refs)
		FROM image_facts_live`,
	"snapshots": `SELECT * FROM snapshots`,
}

// dump renders a query's rows as sorted lines.
func dump(t *testing.T, svc *Service, q string) []string {
	t.Helper()
	lines, err := svc.strings(ctx, `SELECT t::VARCHAR AS line FROM (`+q+`) t ORDER BY line`)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return lines
}

// forceFull throws away everything a refresh derives, so the next refresh
// rebuilds it from scratch.
func forceFull(t testing.TB, svc *Service) {
	t.Helper()
	for _, q := range []string{
		`DELETE FROM edges`, `DELETE FROM phantoms`, `DELETE FROM lineage`, `DELETE FROM staleness`,
		`DELETE FROM subtree_stats`, `DELETE FROM snapshots WHERE day = (SELECT max(day) FROM snapshots)`, `DROP VIEW IF EXISTS image_facts_live`,
		`DROP TABLE IF EXISTS image_facts`, `DELETE FROM meta WHERE key IN ('lineage_fingerprint', 'snapshot_key')`,
	} {
		if _, err := svc.db().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	svc.facts.Store(false)
}

// matchesFull checks that the incremental refresh just run left exactly
// what a full refresh of the same index produces.
func matchesFull(t *testing.T, svc *Service, round string) {
	t.Helper()
	got := map[string][]string{}
	for name, q := range derived {
		got[name] = dump(t, svc, q)
	}
	forceFull(t, svc)
	if err := svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	for name, q := range derived {
		want := dump(t, svc, q)
		if strings.Join(got[name], "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: %s differs from a full refresh:\nincremental only: %v\nfull only: %v",
				round, name, missing(got[name], want), missing(want, got[name]))
		}
	}
}

// missing returns the lines of a that are not in b, at most five.
func missing(a, b []string) []string {
	in := map[string]bool{}
	for _, l := range b {
		in[l] = true
	}
	var out []string
	for _, l := range a {
		if !in[l] && len(out) < 5 {
			out = append(out, l)
		}
	}
	return out
}

// write stores a batch.
func write(t *testing.T, st *store.Store, b *store.Batch) {
	t.Helper()
	if err := st.Write(ctx, b); err != nil {
		t.Fatal(err)
	}
}

// tagged returns a batch that pushes img under tag on day.
func tagged(img *model.Image, tag string, day int) *store.Batch {
	fixture.Created(img, day)
	t := fixture.Tag(img, tag, day)
	return &store.Batch{Images: []*model.Image{img}, Tags: []model.Tag{t}, Events: []model.TagEvent{fixture.Event(t.Repo, tag, img.Digest, day)}}
}

// TestIncrementalRefreshMatchesFull applies rounds of the changes a crawl
// brings and checks after each that the incremental refresh agrees, table
// for table, with rebuilding everything.
func TestIncrementalRefreshMatchesFull(t *testing.T) {
	t.Parallel()
	st := fixture.Open(t, fixture.Org(300))
	svc := New(st)
	svc.Lineage = lineage.Options{Labels: []string{"base"}}
	svc.Required = []string{"team"}
	day := 0
	svc.Now = func() time.Time { return fixture.Epoch.AddDate(0, 0, day) }
	if err := svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if st := svc.Status().LastRefresh; st == nil || !st.Full {
		t.Fatalf("the first refresh should be full: %+v", st)
	}

	// Nothing changed: lineage is skipped and no image row is rebuilt.
	if err := svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if st := svc.Status().LastRefresh; st.Full || !st.LineageSkipped || st.FactsRebuilt != 0 || !st.SnapshotSkipped {
		t.Fatalf("an unchanged refresh did work: %+v", st)
	}
	matchesFull(t, svc, "unchanged")

	rounds := []struct {
		name  string
		apply func()
	}{
		{"new apps on existing bases", func() {
			for i := range 20 {
				img := fixture.Label(fixture.Image(fmt.Sprintf("new-app-%d", i), fmt.Sprintf("reg/team-%d/new-%d", i%3, i), "os-0", "lang-0-0", fmt.Sprintf("new-app-%d", i)), "team", "team-new")
				write(t, st, tagged(img, "v1", day))
			}
		}},
		{"two apps sharing a layer no image ends at", func() {
			for i := range 2 {
				write(t, st, tagged(fixture.Image(fmt.Sprintf("shared-child-%d", i), "reg/shared/app", "os-1", "lang-1-1", "shared-mid", fmt.Sprintf("shared-child-%d", i)), fmt.Sprintf("c%d", i), day))
			}
		}},
		{"the shared layer is pushed as a base", func() {
			write(t, st, tagged(fixture.Image("shared-mid", "reg/shared/base", "os-1", "lang-1-1", "shared-mid"), "latest", day))
		}},
		{"a base tag moves to a rebuild", func() {
			write(t, st, tagged(fixture.Image("lang-0-0-v2", "reg/base/lang-0", "os-0", "lang-0-0", "patch"), "os0", day))
		}},
		{"app tags move and one is deleted", func() {
			for i := range 5 {
				write(t, st, tagged(fixture.Image(fmt.Sprintf("app-%d-v2", i), fmt.Sprintf("reg/team-%d/app-%d", i%25, i/10), "os-0", "lang-0-0", fmt.Sprintf("app-%d-v2", i)), fmt.Sprintf("v%d", i%10), day))
			}
			gone := model.Tag{Repo: "reg/team-0/new-0", Tag: "v1"}
			write(t, st, &store.Batch{DeleteTags: []model.Tag{gone}, Events: []model.TagEvent{{Repo: gone.Repo, Tag: gone.Tag, SeenAt: fixture.Epoch.AddDate(0, 0, day), Source: "crawl"}}})
		}},
		{"referrers, push and pull times", func() {
			app := fixture.Digest("manifest:app-7")
			write(t, st, &store.Batch{
				Referrers: []model.Referrer{{Subject: app, Digest: fixture.Digest("sbom-7"), ArtifactType: "application/spdx+json", Kind: model.KindSBOM}},
				Pushes: []model.Push{
					{Repo: "reg/team-7/app-0", Digest: app, PushedAt: fixture.Epoch.AddDate(0, 0, day-1), LastPulled: fixture.Epoch.AddDate(0, 0, day)},
					{Repo: "reg/team-8/app-0", Digest: fixture.Digest("manifest:app-8"), PushedAt: fixture.Epoch.AddDate(0, 0, day-2)},
				},
			})
		}},
		{"a multi-platform index is tagged and signed", func() {
			arm := fixture.Image("arm-app", "reg/multi/app", "os-2", "lang-2-2", "arm-app")
			arm.Platform = model.Platform{OS: "linux", Arch: "arm64"}
			amd := fixture.Image("amd-app", "reg/multi/app", "os-2", "lang-2-2", "amd-app")
			idx := fixture.Digest("index:multi")
			arm.IndexDigest, amd.IndexDigest = idx, idx
			write(t, st, &store.Batch{
				Images: []*model.Image{arm, amd},
				Index: []model.IndexEntry{
					{IndexDigest: idx, Digest: arm.Digest, Platform: arm.Platform},
					{IndexDigest: idx, Digest: amd.Digest, Platform: amd.Platform},
				},
				Tags:      []model.Tag{{Repo: "reg/multi/app", Tag: "1.0", Digest: idx, UpdatedAt: fixture.Epoch.AddDate(0, 0, day)}},
				Events:    []model.TagEvent{fixture.Event("reg/multi/app", "1.0", idx, day)},
				Referrers: []model.Referrer{{Subject: idx, Digest: fixture.Digest("sig-multi"), ArtifactType: "application/vnd.dev.cosign.signature", Kind: model.KindSignature}},
			})
		}},
		{"a label declares a parent by tag", func() {
			img := fixture.Label(fixture.Image("declared", "reg/declared/app", "unrelated-root", "declared"), "base", "reg/base/os:v1")
			write(t, st, tagged(img, "v1", day))
		}},
		{"an SBOM is attached to an image nothing else changed", func() {
			write(t, st, &store.Batch{Referrers: []model.Referrer{{Subject: fixture.Digest("manifest:app-42"), Digest: fixture.Digest("sbom-42"), ArtifactType: "application/spdx+json", Kind: model.KindSBOM}}})
		}},
		{"the index gets a second tag", func() {
			// Its platform images' refs change; nothing else does.
			idx := fixture.Digest("index:multi")
			write(t, st, &store.Batch{
				Tags:   []model.Tag{{Repo: "reg/multi/app", Tag: "stable", Digest: idx, UpdatedAt: fixture.Epoch.AddDate(0, 0, day)}},
				Events: []model.TagEvent{fixture.Event("reg/multi/app", "stable", idx, day)},
			})
		}},
		{"the tag a label names moves to another indexed image", func() {
			// No image is added, yet the declared parent changes.
			os2 := fixture.Digest("manifest:os-2")
			write(t, st, &store.Batch{
				Tags:   []model.Tag{{Repo: "reg/base/os", Tag: "v1", Digest: os2, UpdatedAt: fixture.Epoch.AddDate(0, 0, day)}},
				Events: []model.TagEvent{fixture.Event("reg/base/os", "v1", os2, day)},
			})
		}},
		{"a root base gets a second tag on the same digest", func() {
			// Nothing drifts, but the root_refs of everything built on it
			// gain the tag; for grandchildren that is the only change.
			base := fixture.Image("os-1", "reg/base/os", "os-1")
			tg := fixture.Tag(base, "stable", day)
			write(t, st, &store.Batch{Tags: []model.Tag{tg}, Events: []model.TagEvent{fixture.Event(tg.Repo, tg.Tag, tg.Digest, day)}})
		}},
		{"scans are listed, then findings fetched", func() {
			done := fixture.Epoch.AddDate(0, 0, day)
			write(t, st, &store.Batch{Scans: []model.Scan{
				{Digest: fixture.Digest("manifest:app-3"), Source: "ecr", Status: model.ScanComplete, CompletedAt: done, Critical: 2},
				{Digest: fixture.Digest("manifest:os-0"), Source: "ecr", Status: model.ScanComplete, CompletedAt: done, High: 1},
			}})
			write(t, st, &store.Batch{Scans: []model.Scan{
				{Digest: fixture.Digest("manifest:app-3"), Source: "ecr", Status: model.ScanComplete, CompletedAt: done, Critical: 2,
					HasFindings: true, FetchedAt: done, Findings: []model.Finding{{ID: "CVE-1", Package: "a", Severity: "critical", FixedIn: "2"}}},
			}})
		}},
		{"a rescan changes counts", func() {
			write(t, st, &store.Batch{Scans: []model.Scan{
				{Digest: fixture.Digest("manifest:os-0"), Source: "ecr", Status: model.ScanComplete, CompletedAt: fixture.Epoch.AddDate(0, 0, day+1), High: 3},
			}})
		}},
		{"only time passes", func() { day += 30 }},
	}
	for _, r := range rounds {
		r.apply()
		if err := svc.Refresh(ctx); err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		st := svc.Status().LastRefresh
		if st.Full {
			t.Fatalf("%s: refresh was full", r.name)
		}
		// Each change touches part of the index (a root tag moving reaches
		// every image under the old and new root); only the passing
		// of time can reach every image.
		var images int
		if err := svc.db().QueryRow(`SELECT count(*) FROM images`).Scan(&images); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %+v", r.name, *st)
		if r.name != "only time passes" && st.FactsRebuilt > images/2 {
			t.Errorf("%s: rebuilt %d of %d image rows", r.name, st.FactsRebuilt, images)
		}
		matchesFull(t, svc, r.name)
	}
}

// TestRefreshRebuildsOutdatedFacts checks that an image_facts table whose
// columns no longer match image_view, as after an upgrade, is rebuilt in
// full.
func TestRefreshRebuildsOutdatedFacts(t *testing.T) {
	t.Parallel()
	svc := New(fixture.Open(t, fixture.Org(40)))
	if err := svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP VIEW image_facts_live`, `CREATE OR REPLACE TABLE image_facts AS SELECT * EXCLUDE (first_seen) FROM image_facts`} {
		if _, err := svc.db().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.db().Exec(factsLiveView); err != nil {
		t.Fatal(err)
	}
	if err := svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if st := svc.Status().LastRefresh; !st.Full {
		t.Fatalf("an outdated image_facts was not rebuilt: %+v", st)
	}
	// A service opened on a file whose facts lack the live view creates it.
	if _, err := svc.db().Exec(`DROP VIEW image_facts_live`); err != nil {
		t.Fatal(err)
	}
	if again := New(svc.Store); again.view() != "image_facts_live" {
		t.Fatalf("view = %s", again.view())
	}
}

// TestCancelledRefreshLeavesIndexUsable cancels refreshes part way, as
// happens to a refresh during a crawl when the crawl ends, and checks the
// next refresh still works. A cancelled statement used to leave its
// transaction open on a pooled connection, so the next refresh failed.
func TestCancelledRefreshLeavesIndexUsable(t *testing.T) {
	t.Parallel()
	st := fixture.Open(t, fixture.Org(3000))
	svc := New(st)
	for i := range 12 {
		cctx, cancel := context.WithTimeout(ctx, time.Duration(i*15)*time.Millisecond)
		_ = svc.Refresh(cctx)
		cancel()
		write(t, st, tagged(fixture.Image(fmt.Sprintf("cancel-%d", i), "reg/cancel/app", "os-0", "lang-0-0", fmt.Sprintf("cancel-%d", i)), fmt.Sprintf("v%d", i), 0))
	}
	if err := svc.Refresh(ctx); err != nil {
		t.Fatalf("refresh after cancelled ones: %v", err)
	}
	matchesFull(t, svc, "after cancelled refreshes")
}
