package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"

	"github.com/rossedman/strata/internal/crawl"
	"github.com/rossedman/strata/internal/fixture"
	"github.com/rossedman/strata/internal/graph"
	"github.com/rossedman/strata/internal/testreg"
)

// env is an indexed test registry.
type env struct {
	svc *Service
	reg *testreg.Registry
	// oldGoAMD is the go amd64 digest before the base was rebuilt.
	oldGoAMD string
}

// index crawls the registry and refreshes derived views.
func (e *env) index(t *testing.T) {
	t.Helper()
	_, err := e.svc.Index(context.Background(), crawl.Options{
		Registry: e.reg.Host, Insecure: true, Referrers: true, Keychain: authn.NewMultiKeychain(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

// setup indexes the test registry, rebuilds the go base and pushes a new
// api build on it, then indexes again so tag history has moves.
func setup(t *testing.T) *env {
	t.Helper()
	r := testreg.Start(t)
	st := fixture.Open(t, nil)
	svc := New(st)
	svc.Required = []string{"team", "source"}
	svc.Synonyms = map[string]string{"owner": "team"}
	e := &env{svc: svc, reg: r, oldGoAMD: r.Digest("go-amd64")}
	e.index(t)

	// Rebuild go (amd64 only) on debian and ship api v2 on the new base.
	goAMD := r.Layered("debian", "go-amd64-v2", time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC), nil, "RUN install go 1.22.7")
	idx := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: goAMD, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}})
	r.PushIndex("base/go:1.22", idx)
	api2 := r.Layered("go-amd64-v2", "api2", time.Date(2025, 9, 2, 0, 0, 0, 0, time.UTC),
		&v1.Config{Labels: map[string]string{"owner": "payments", "deprecated": "true"}}, "RUN build api v2")
	r.Push("payments/api:latest", api2)
	// The same path under another repo makes short refs ambiguous.
	r.Push("mirror/payments/api:1.0", r.Images["worker"])
	e.index(t)
	return e
}

var ctx = context.Background()

func TestResolve(t *testing.T) {
	t.Parallel()
	e := setup(t)
	h := e.reg.Host
	api := e.reg.Digest("api")
	tests := []struct {
		ref, platform, want string
	}{
		{h + "/payments/api:1.0", "", api},
		{api, "", api},
		{api[:19], "", api},
		{h + "/payments/api@" + api, "", api},
		{"payments/worker:v1", "", e.reg.Digest("worker")},
		{h + "/base/go:1.22", "", e.reg.Digest("go-amd64-v2")},
		{"base/debian:12", "linux/amd64", e.reg.Digest("debian")},
	}
	for _, tt := range tests {
		got, err := e.svc.Resolve(ctx, tt.ref, tt.platform)
		if err != nil || got != tt.want {
			t.Errorf("Resolve(%q) = %s, %v; want %s", tt.ref, got, err, tt.want)
		}
	}
	errs := []struct {
		ref, platform   string
		input, notFound bool
	}{
		{"nope:1", "", false, true},
		{"", "", true, false},
		{"payments/api:1.0", "", true, false}, // matches two repos
		{h + "/base/go:1.22", "linux/s390x", true, false},
	}
	for _, tt := range errs {
		_, err := e.svc.Resolve(ctx, tt.ref, tt.platform)
		if err == nil || IsInputError(err) != tt.input || errors.Is(err, ErrNotFound) != tt.notFound {
			t.Errorf("Resolve(%q, %q) error = %v", tt.ref, tt.platform, err)
		}
	}
	// An index with several platforms picks the configured default, then
	// the one asked for.
	e2 := setupIndexOnly(t)
	amd, err := e2.svc.Resolve(ctx, e2.reg.Host+"/base/go:1.22", "")
	if err != nil || amd != e2.reg.Digest("go-amd64") {
		t.Fatalf("default platform = %s, %v", amd, err)
	}
	arm, err := e2.svc.Resolve(ctx, e2.reg.Host+"/base/go:1.22", "linux/arm64/v8")
	if err != nil || arm != e2.reg.Digest("go-arm64") {
		t.Fatalf("arm64 = %s, %v", arm, err)
	}
	e2.svc.Platform = "windows/amd64"
	if first, err := e2.svc.Resolve(ctx, e2.reg.Host+"/base/go:1.22", ""); err != nil || first == "" {
		t.Fatalf("fallback platform = %s, %v", first, err)
	}
}

// setupIndexOnly indexes the test registry once, without changes.
func setupIndexOnly(t *testing.T) *env {
	t.Helper()
	r := testreg.Start(t)
	e := &env{svc: New(fixture.Open(t, nil)), reg: r}
	e.index(t)
	return e
}

func TestExplain(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ex, err := e.svc.Explain(ctx, e.reg.Host+"/payments/api:1.0", "", true)
	if err != nil {
		t.Fatal(err)
	}
	im := ex.Image
	if im.Label != e.reg.Host+"/payments/api:1.0" || im.Platform != "linux/amd64" || im.Created == nil || im.AgeDays == nil {
		t.Fatalf("image = %+v", im)
	}
	if im.Config.EnvValues != nil {
		t.Fatalf("env values stored without an allowlist: %v", im.Config.EnvValues)
	}
	if im.Labels["team"] != "payments" || len(ex.Layers) != 3 || len(ex.History) != 3 {
		t.Fatalf("labels=%v layers=%d history=%d", im.Labels, len(ex.Layers), len(ex.History))
	}
	if ex.Lineage.Status != "derived" || ex.Lineage.Parent != e.oldGoAMD || ex.Lineage.Generation != 2 {
		t.Fatalf("lineage = %+v", ex.Lineage)
	}
	if len(ex.Lineage.Ancestry.Nodes) != 3 {
		t.Fatalf("ancestry nodes = %d", len(ex.Lineage.Ancestry.Nodes))
	}
	if len(ex.Referrers) != 2 {
		t.Fatalf("referrers = %+v", ex.Referrers)
	}
	findings := strings.Join(ex.Findings, "\n")
	for _, want := range []string{"AWS_SECRET_ACCESS_KEY looks like a secret", "runs as root", "entrypoint starts through a shell"} {
		if !strings.Contains(findings, want) {
			t.Errorf("findings lack %q: %s", want, findings)
		}
	}
	// api 1.0 is on the old go base, which its tag has moved off.
	if ex.Staleness.Verdict != "drifting" || ex.Staleness.BaseDriftGenerations != 1 || ex.Staleness.BaseTag == "" {
		t.Fatalf("staleness = %+v", ex.Staleness)
	}

	// The worker declares its base in an annotation as well.
	w, err := e.svc.Explain(ctx, "payments/worker:v1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	sources := []string{}
	for _, edge := range w.Lineage.Edges {
		sources = append(sources, edge.Source)
	}
	if !slices.Contains(sources, "layers") || !slices.Contains(sources, "annotation") {
		t.Fatalf("worker edges = %+v", w.Lineage.Edges)
	}
	if !w.Lineage.Conflict {
		t.Fatal("worker's annotation names the rebuilt base, which should conflict with its layers")
	}

	// debian is the root with descendants; go arm64 is unrelated.
	d, err := e.svc.Explain(ctx, "base/debian:12", "", false)
	if err != nil || d.Lineage.Status != "root" || d.Lineage.Descendants < 3 {
		t.Fatalf("debian lineage = %+v, %v", d.Lineage, err)
	}
	if _, err := e.svc.Explain(ctx, "nope:1", "", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing image error = %v", err)
	}
}

func TestFindings(t *testing.T) {
	t.Parallel()
	ex := &Explanation{Image: ImageInfo{Config: ConfigInfo{User: "app", Healthcheck: false, EnvKeys: []string{"GITHUB_TOKEN"}}},
		Lineage: LineageInfo{Status: "unknown", Conflict: true}}
	got := strings.Join(findings(ex), "\n")
	for _, want := range []string{"GITHUB_TOKEN", "no healthcheck", "disagrees", "lineage unknown"} {
		if !strings.Contains(got, want) {
			t.Errorf("findings lack %q: %s", want, got)
		}
	}
	if strings.Contains(got, "root") {
		t.Fatalf("non-root user flagged: %s", got)
	}
}

func TestAncestryAndDescendants(t *testing.T) {
	t.Parallel()
	e := setup(t)
	h := e.reg.Host
	g, err := e.svc.Ancestry(ctx, "payments/worker:v1", "")
	if err != nil {
		t.Fatal(err)
	}
	if g.Kind != graph.Ancestry || g.Root != e.reg.Digest("worker") {
		t.Fatalf("ancestry = %+v", g)
	}
	conflict := false
	for _, edge := range g.Edges {
		conflict = conflict || edge.Conflict
	}
	if !conflict || len(g.Nodes) != 4 {
		t.Fatalf("ancestry with conflict: %d nodes, conflict=%v", len(g.Nodes), conflict)
	}
	if _, err := e.svc.Ancestry(ctx, "nope:1", ""); err == nil {
		t.Fatal("Ancestry of a missing ref succeeded")
	}

	// Today's go:1.22 has only the new api on it.
	res, err := e.svc.Descendants(ctx, h+"/base/go:1.22", DescendantOptions{Depth: 0})
	if err != nil {
		t.Fatal(err)
	}
	if res.Images != 1 || len(res.Versions) != 1 {
		t.Fatalf("current descendants = %d, versions %d", res.Images, len(res.Versions))
	}
	// Every version the tag held adds the old base and its children.
	all, err := e.svc.Descendants(ctx, h+"/base/go:1.22", DescendantOptions{Depth: 0, AllVersions: true})
	if err != nil {
		t.Fatal(err)
	}
	if all.Images < 3 || len(all.Versions) < 2 {
		t.Fatalf("all versions: images=%d versions=%+v", all.Images, all.Versions)
	}
	var old *BaseVersion
	for i := range all.Versions {
		if all.Versions[i].Digest == e.oldGoAMD {
			old = &all.Versions[i]
		}
	}
	if old == nil || old.Source != "tag_history" || old.Children < 2 {
		t.Fatalf("old base version = %+v", old)
	}

	// A versions file adds bases by digest or ref.
	file := filepath.Join(t.TempDir(), "versions.txt")
	if err := os.WriteFile(file, []byte("# old go\n"+e.oldGoAMD+"\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile, err := e.svc.Descendants(ctx, h+"/base/go:1.22", DescendantOptions{Depth: 1, VersionsFrom: file, Budget: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(fromFile.Versions) != 2 || fromFile.Versions[1].Confidence != "declared" {
		t.Fatalf("versions from file = %+v", fromFile.Versions)
	}
	if err := os.WriteFile(file, []byte("nope:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Descendants(ctx, h+"/base/go:1.22", DescendantOptions{VersionsFrom: file}); err == nil {
		t.Fatal("bad versions file accepted")
	}
	if _, err := e.svc.Descendants(ctx, h+"/base/go:1.22", DescendantOptions{VersionsFrom: "/nope"}); err == nil {
		t.Fatal("missing versions file accepted")
	}
	if _, err := e.svc.Descendants(ctx, "nope:1", DescendantOptions{}); err == nil {
		t.Fatal("Descendants of a missing ref succeeded")
	}
}

func TestQuery(t *testing.T) {
	t.Parallel()
	e := setup(t)
	tb, err := e.svc.Query(ctx, QueryOptions{Filter: `label.team = "payments"`, Fields: []string{"repo", "tag", "created", "labels_x"}})
	if err == nil || !IsInputError(err) {
		t.Fatalf("unknown field error = %v (%T)", err, tb)
	}
	tb, err = e.svc.Query(ctx, QueryOptions{Filter: `label.team = "payments"`, Sort: []string{"-created"}, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(tb.Rows) != 2 || len(tb.Columns) != len(DefaultFields) {
		t.Fatalf("rows = %v", tb.Rows)
	}
	if _, ok := tb.Rows[0][5].(int64); !ok {
		t.Fatalf("size is %T, want int64", tb.Rows[0][5])
	}
	if _, ok := tb.Rows[0][4].(time.Time); !ok {
		t.Fatalf("created is %T, want time.Time", tb.Rows[0][4])
	}
	g, err := e.svc.Query(ctx, QueryOptions{GroupBy: "tag"})
	if err != nil || g.Columns[0] != "tag" {
		t.Fatalf("group by tag = %+v, %v", g, err)
	}
	g, err = e.svc.Query(ctx, QueryOptions{GroupBy: "label.team", Limit: 2})
	if err != nil || len(g.Rows) != 2 {
		t.Fatalf("group by label = %+v, %v", g, err)
	}
	for _, bad := range []QueryOptions{{Filter: "nope = 1"}, {GroupBy: "nope"}, {Sort: []string{"nope"}}} {
		if _, err := e.svc.Query(ctx, bad); !IsInputError(err) {
			t.Errorf("Query(%+v) error = %v", bad, err)
		}
	}
	stop := errors.New("stop")
	err = e.svc.QueryEach(ctx, QueryOptions{}, func([]string) error { return stop }, nil)
	if !errors.Is(err, stop) {
		t.Fatalf("header error not returned: %v", err)
	}
	err = e.svc.QueryEach(ctx, QueryOptions{}, func([]string) error { return nil }, func([]any) error { return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("emit error not returned: %v", err)
	}

	qg, err := e.svc.QueryGraph(ctx, `label.team = "payments"`, 0)
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, n := range qg.Nodes {
		if n.Matched {
			matched++
		}
	}
	if matched != 2 || len(qg.Nodes) < 4 {
		t.Fatalf("query graph: %d nodes, %d matched", len(qg.Nodes), matched)
	}
	if _, err := e.svc.QueryGraph(ctx, `nope`, 10); !IsInputError(err) {
		t.Fatalf("bad graph filter error = %v", err)
	}
}

func TestReports(t *testing.T) {
	t.Parallel()
	e := setup(t)
	st, err := e.svc.Stale(ctx, "")
	if err != nil || len(st.Rows) != 1 || st.Columns[0] != "scope" {
		t.Fatalf("stale = %+v, %v", st, err)
	}
	for _, g := range []string{"repo", "root", "label.team"} {
		if st, err := e.svc.Stale(ctx, g); err != nil || len(st.Rows) == 0 {
			t.Errorf("stale by %s = %+v, %v", g, st, err)
		}
	}
	if _, err := e.svc.Stale(ctx, "tag"); !IsInputError(err) {
		t.Fatalf("stale by a list field error = %v", err)
	}
	roots, err := e.svc.Roots(ctx)
	if err != nil || len(roots) == 0 || roots[0].Descendants < 3 || roots[0].Label == "" {
		t.Fatalf("roots = %+v, %v", roots, err)
	}
	mg, err := e.svc.RootsGraph(ctx)
	if err != nil || len(mg.Nodes) < 4 {
		t.Fatalf("roots graph = %+v, %v", mg, err)
	}

	sum, err := e.svc.Summary(ctx, "label.team")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Total.Manifests < 6 || sum.Total.DedupBytes == 0 || sum.Total.DedupBytes > sum.Total.ApparentBytes {
		t.Fatalf("summary total = %+v", sum.Total)
	}
	if len(sum.Groups) < 2 || sum.Platforms["linux/amd64"] == 0 || sum.Compliance["team"] == 0 {
		t.Fatalf("summary = %+v", sum)
	}
	var excl int64
	for _, g := range sum.Groups {
		excl += g.ExclusiveBytes
		if g.ExclusiveBytes > g.DedupBytes || g.SharedBytes > g.DedupBytes {
			t.Fatalf("group bytes out of order: %+v", g)
		}
	}
	if excl > sum.Total.DedupBytes {
		t.Fatalf("exclusive bytes %d exceed deduplicated %d", excl, sum.Total.DedupBytes)
	}
	if _, err := e.svc.Summary(ctx, "nope"); !IsInputError(err) {
		t.Fatalf("summary by unknown field error = %v", err)
	}

	keys, err := e.svc.Labels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]LabelKey{}
	for _, k := range keys {
		byKey[k.Key] = k
	}
	if byKey["team"].Images != 2 || !byKey["team"].Required || !byKey["source"].Required || byKey["source"].Images != 0 {
		t.Fatalf("label keys = %+v", keys)
	}
	d, err := e.svc.Label(ctx, "team")
	if err != nil || d.Values["payments"] != 2 || d.FirstSeen == nil || d.Kind != "enum" || len(d.Lacking) == 0 {
		t.Fatalf("label detail = %+v, %v", d, err)
	}
	similar, err := e.svc.SimilarKeys(ctx)
	if err != nil || len(similar) != 1 || !slices.Equal(similar[0], []string{"owner", "team"}) {
		t.Fatalf("similar keys = %v, %v", similar, err)
	}
}

func TestLayersAndBases(t *testing.T) {
	t.Parallel()
	e := setup(t)
	top, err := e.svc.TopLayers(ctx, "shares", 3)
	if err != nil || len(top) != 3 || top[0].Images < top[1].Images {
		t.Fatalf("top by shares = %+v, %v", top, err)
	}
	if bySize, err := e.svc.TopLayers(ctx, "size", 0); err != nil || len(bySize) == 0 {
		t.Fatalf("top by size = %+v, %v", bySize, err)
	}
	if _, err := e.svc.TopLayers(ctx, "color", 1); err == nil {
		t.Fatal("bad --by accepted")
	}
	bloat, err := e.svc.Bloat(ctx, false, 1, 50)
	if err != nil || len(bloat) == 0 {
		t.Fatalf("bloat = %+v, %v", bloat, err)
	}
	for _, l := range bloat {
		if l.Images != 1 {
			t.Fatalf("bloat layer used by %d images", l.Images)
		}
	}
	if perRepo, err := e.svc.Bloat(ctx, true, 1, 50); err != nil || len(perRepo) < len(bloat)-2 {
		t.Fatalf("bloat per repo = %d, %v", len(perRepo), err)
	}
	clusters, err := e.svc.SimilarBases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The old and new go bases differ only in their top layer.
	found := false
	for _, c := range clusters {
		if len(c.Bases) == 2 && c.SharedLayers == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("similar bases = %+v", clusters)
	}
	dd := dedupeClusters([]BaseCluster{{SharedLayers: 1, Bases: []string{"a", "b"}}, {SharedLayers: 2, Bases: []string{"a", "b"}}})
	if len(dd) != 1 || dd[0].SharedLayers != 2 {
		t.Fatalf("dedupeClusters = %+v", dd)
	}
}

func TestSnapshotsDiffTrend(t *testing.T) {
	t.Parallel()
	e := setup(t)
	if _, err := e.svc.Diff(ctx, 7*24*time.Hour, "", 90); err != nil {
		t.Fatal(err)
	}
	// Pretend the first snapshot was a week ago.
	if _, err := e.svc.Store.DB().Exec(`UPDATE snapshots SET day = day - INTERVAL 7 DAY`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Store.DB().Exec(`DELETE FROM snapshots WHERE group_by = 'repo' AND group_value LIKE '%worker'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Store.DB().Exec(`UPDATE snapshots SET value = value / 2 WHERE metric = 'coverage_pct'`); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	d, err := e.svc.Diff(ctx, 7*24*time.Hour, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, c := range d.Changes {
		kinds[c.Kind] = true
	}
	if !kinds["new repo"] || !kinds["label coverage"] {
		t.Fatalf("diff kinds = %v (%+v)", kinds, d.Changes)
	}
	if len(d.DarkRepos) == 0 {
		t.Fatal("with dark-days 0 every tagged repo is dark")
	}
	if _, err := e.svc.Diff(ctx, time.Hour, "repo", 90); err != nil {
		t.Fatal(err)
	}
	pts, err := e.svc.Trend(ctx, "manifests", "")
	if err != nil || len(pts) != 2 {
		t.Fatalf("trend = %+v, %v", pts, err)
	}
	if pts, err := e.svc.Trend(ctx, "dedup_bytes", "repo"); err != nil || len(pts) == 0 {
		t.Fatalf("trend by repo = %d, %v", len(pts), err)
	}
	if _, err := e.svc.Trend(ctx, "nope", ""); !IsInputError(err) {
		t.Fatalf("unknown metric error = %v", err)
	}
	if _, err := e.svc.Trend(ctx, "manifests", "label.nope"); err == nil {
		t.Fatal("trend by an unrecorded group succeeded")
	}
	empty := New(fixture.Open(t, nil))
	if _, err := empty.Diff(ctx, time.Hour, "", 1); err == nil {
		t.Fatal("diff without snapshots succeeded")
	}
}

func TestNormalizeKey(t *testing.T) {
	t.Parallel()
	tests := map[string]string{"team": "team", "Team": "team", "team_name": "team", "TEAM-ID": "team", "t.e-a_m": "team", "id": "id", "name": "name"}
	for in, want := range tests {
		if got := NormalizeKey(in); got != want {
			t.Errorf("NormalizeKey(%q) = %q, want %q", in, got, want)
		}
	}
	got := similarKeys([]string{"team", "Team", "owner", "env"}, map[string]string{"owner": "team"})
	if len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("similarKeys = %v", got)
	}
}

func TestInputError(t *testing.T) {
	t.Parallel()
	if input(nil) != nil {
		t.Fatal("input(nil) != nil")
	}
	err := input(errors.New("bad"))
	if !IsInputError(err) || err.Error() != "bad" || errors.Unwrap(err).Error() != "bad" {
		t.Fatalf("input error = %v", err)
	}
	if IsInputError(errors.New("x")) {
		t.Fatal("plain error is not an input error")
	}
}

func TestIndexErrors(t *testing.T) {
	t.Parallel()
	svc := New(fixture.Open(t, nil))
	if _, err := svc.Index(ctx, crawl.Options{Registry: "127.0.0.1:1", Insecure: true, Keychain: authn.NewMultiKeychain()}); err == nil {
		t.Fatal("index of an unreachable registry succeeded")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := svc.Refresh(canceled); err == nil {
		t.Fatal("refresh on a canceled context succeeded")
	}
}

func BenchmarkResolve(b *testing.B) {
	r := testreg.Start(b)
	svc := New(fixture.Open(b, nil))
	if _, err := svc.Index(ctx, crawl.Options{Registry: r.Host, Insecure: true, Keychain: authn.NewMultiKeychain()}); err != nil {
		b.Fatal(err)
	}
	ref := r.Host + "/payments/api:1.0"
	b.ReportAllocs()
	for b.Loop() {
		if _, err := svc.Resolve(ctx, ref, ""); err != nil {
			b.Fatal(err)
		}
	}
}
