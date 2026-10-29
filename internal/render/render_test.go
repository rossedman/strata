package render

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rossedman/strata/internal/graph"
	"github.com/rossedman/strata/internal/model"
	"github.com/rossedman/strata/internal/service"
)

var (
	day  = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cols = []string{"repo", "tag", "digest", "created", "size", "stale", "ok", "n", "pct", "none", "other"}
	row  = []any{"reg/app", []string{"v1", "latest"}, "sha256:" + strings.Repeat("a", 64), day, int64(2048), "stale", true, int64(3), 1.5, nil, struct{}{}}
)

func TestRowWriters(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := WriteRows(&b, Table, cols, [][]any{row}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"reg/app", "v1, latest", "sha256:aaaaaaaaaaaa", "2026-01-02", "2.0 KiB", "stale", "true", "1.5", "-"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	b.Reset()
	if err := WriteRows(&b, Table, cols, nil); err != nil || !strings.Contains(b.String(), "no results") {
		t.Fatalf("empty table = %q, %v", b.String(), err)
	}

	b.Reset()
	if err := WriteRows(&b, JSON, cols[:2], [][]any{row[:2]}); err != nil {
		t.Fatal(err)
	}
	var objs []map[string]any
	if err := json.Unmarshal(b.Bytes(), &objs); err != nil || objs[0]["repo"] != "reg/app" {
		t.Fatalf("json = %s, %v", b.String(), err)
	}

	b.Reset()
	if err := WriteRows(&b, NDJSON, cols[:2], [][]any{row[:2], row[:2]}); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(b.String()), "\n"); len(lines) != 2 {
		t.Fatalf("ndjson lines = %d", len(lines))
	}

	b.Reset()
	if err := WriteRows(&b, CSV, cols, [][]any{row}); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(&b).ReadAll()
	if err != nil || len(recs) != 2 || recs[1][1] != "v1;latest" || recs[1][3] != "2026-01-02T03:04:05Z" || recs[1][9] != "" {
		t.Fatalf("csv = %v, %v", recs, err)
	}
	if _, err := NewRowWriter(&b, "xml"); err == nil {
		t.Fatal("unknown format accepted")
	}
	if err := WriteRows(&b, "xml", nil, nil); err == nil {
		t.Fatal("WriteRows accepted an unknown format")
	}
}

func TestCell(t *testing.T) {
	t.Parallel()
	tests := []struct {
		col  string
		v    any
		want string
	}{
		{"created", time.Time{}, "-"},
		{"digest", []string{"sha256:" + strings.Repeat("b", 64)}, "sha256:bbbbbbbbbbbb"},
		{"x", "", "-"},
		{"x", "phantom:sha256:" + strings.Repeat("c", 64), "sha256:cccccccccccc"},
		{"dedup_bytes", int64(3 << 30), "3.0 GiB"},
		{"x", false, "false"},
	}
	for _, tt := range tests {
		if got := Cell(tt.col, tt.v); got != tt.want {
			t.Errorf("Cell(%s, %v) = %q, want %q", tt.col, tt.v, got, tt.want)
		}
	}
	in := []string{"sha256:" + strings.Repeat("d", 64)}
	Cell("digest", in)
	if len(in[0]) != 71 {
		t.Fatal("Cell modified its input")
	}
}

// chain is an ancestry graph with every node kind and edge style.
func chain() *graph.Graph {
	d := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
	return &graph.Graph{
		Kind: graph.Ancestry, Root: d("a"), Title: "ancestry",
		Nodes: []graph.Node{
			{ID: d("a"), Label: "reg/app:1.0", Kind: graph.KindImage, Verdict: "stale", Note: "base drift: 3 gens", Matched: true},
			{ID: d("b"), Label: "reg/base:2", Kind: graph.KindImage, Verdict: "fresh", Depth: 1},
			{ID: model.PhantomPrefix + d("c"), Label: "deleted", Kind: graph.KindPhantom, Depth: 2},
			{ID: d("e"), Label: "debian:12", Kind: graph.KindExternal, Depth: 3},
			{ID: d("f"), Label: "reg/declared:1", Kind: graph.KindImage, Depth: 1},
		},
		Edges: []graph.Edge{
			{Parent: d("b"), Child: d("a"), Source: "layers"},
			{Parent: d("b"), Child: d("a"), Source: "label"},
			{Parent: d("f"), Child: d("a"), Source: "annotation", Conflict: true},
			{Parent: model.PhantomPrefix + d("c"), Child: d("b"), Source: "layers"},
			{Parent: d("e"), Child: model.PhantomPrefix + d("c"), Source: "layers"},
		},
	}
}

// fanGraph is a descendant graph with collapsed and overflow nodes.
func fanGraph() *graph.Graph {
	return &graph.Graph{
		Kind: graph.Descendants, Root: "base", Title: "descendants", Hidden: 4,
		Nodes: []graph.Node{
			{ID: "base", Label: "reg/base:1", Kind: graph.KindImage, Verdict: "fresh"},
			{ID: "c", Label: "team/app ×3", Kind: graph.KindCollapsed, Count: 3, Depth: 1, Members: []string{"a", "b", "c"}},
			{ID: "o", Label: "+4 more", Kind: graph.KindOverflow, Count: 4, Depth: 1},
			{ID: "x", Label: "reg/x:1", Kind: graph.KindImage, Depth: 1, Verdict: "drifting"},
			{ID: "y", Label: "reg/y:1", Kind: graph.KindImage, Depth: 2},
		},
		Edges: []graph.Edge{{Parent: "base", Child: "c", Source: "layers"}, {Parent: "base", Child: "o", Source: "layers"},
			{Parent: "base", Child: "x", Source: "layers"}, {Parent: "x", Child: "y", Source: "label"}},
	}
}

func TestTree(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := WriteGraph(&b, FormatTree, chain()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"reg/app:1.0", "stale (base drift: 3 gens)", "└─ reg/base:2", "layers + label", "(not in registry)", "(external)", "conflict: declared reg/declared:1"} {
		if !strings.Contains(out, want) {
			t.Errorf("tree lacks %q:\n%s", want, out)
		}
	}
	b.Reset()
	if err := WriteGraph(&b, "", fanGraph()); err != nil {
		t.Fatal(err)
	}
	out = b.String()
	for _, want := range []string{"reg/base:1", "└─ +4 more", "├─ team/app ×3", "├─ reg/x:1", "│  └─ reg/y:1", "4 images hidden"} {
		if !strings.Contains(out, want) {
			t.Errorf("descendant tree lacks %q:\n%s", want, out)
		}
	}
}

func TestDiagrams(t *testing.T) {
	t.Parallel()
	for _, g := range []*graph.Graph{chain(), fanGraph(), {Kind: graph.Query, Nodes: []graph.Node{{ID: "a", Label: "a\"q", Kind: graph.KindImage}}}} {
		var m, d, h, j bytes.Buffer
		if err := WriteGraph(&m, Mermaid, g); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(m.String(), "flowchart ") || strings.Contains(m.String(), `a"q`) {
			t.Fatalf("mermaid:\n%s", m.String())
		}
		if err := WriteGraph(&d, Dot, g); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(d.String(), "digraph strata {") || !strings.HasSuffix(d.String(), "}\n") {
			t.Fatalf("dot:\n%s", d.String())
		}
		if err := WriteGraph(&h, HTML, g); err != nil {
			t.Fatal(err)
		}
		html := h.String()
		if !strings.Contains(html, "cytoscape") || strings.Contains(html, "__TITLE__") || strings.Contains(html, "/*__DATA__*/") || strings.Contains(html, `a"q</`) {
			t.Fatal("html page not filled")
		}
		if err := WriteGraph(&j, JSON, g); err != nil {
			t.Fatal(err)
		}
		var back graph.Graph
		if err := json.Unmarshal(j.Bytes(), &back); err != nil || len(back.Nodes) != len(g.Nodes) {
			t.Fatalf("json round trip: %v", err)
		}
	}
	m := new(bytes.Buffer)
	_ = MermaidTo(m, chain())
	if !strings.Contains(m.String(), "-.->") || !strings.Contains(m.String(), "linkStyle 2 stroke:#C8243B") || !strings.Contains(m.String(), `{{"`) {
		t.Fatalf("mermaid styles missing:\n%s", m.String())
	}
	d := new(bytes.Buffer)
	_ = DotTo(d, chain())
	if !strings.Contains(d.String(), "style=dashed") || !strings.Contains(d.String(), "hexagon") || !strings.Contains(d.String(), `color="#C8243B"`) {
		t.Fatalf("dot styles missing:\n%s", d.String())
	}
	src := D2Source(fanGraph())
	if !strings.Contains(src, "direction: right") || !strings.Contains(src, "style.multiple: true") || !strings.Contains(src, "shape: page") {
		t.Fatalf("d2 source:\n%s", src)
	}
	if err := WriteGraph(new(bytes.Buffer), "png", chain()); err == nil {
		t.Fatal("unknown graph format accepted")
	}
}

func TestSVG(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := SVGTo(&b, chain()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "<svg") || !strings.Contains(b.String(), "reg/app:1.0") {
		t.Fatal("svg missing content")
	}
	if err := SVGTo(failWriter{}, fanGraph()); err == nil {
		t.Fatal("write error not returned")
	}
}

// failWriter fails every write.
type failWriter struct{}

// Write always fails.
func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestMapAndTrend(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := MapTo(&b, fanGraph()); err != nil || !strings.Contains(b.String(), "registry map") {
		t.Fatalf("map: %v", err)
	}
	b.Reset()
	series := []Series{
		{Name: "all", Days: []string{"2026-01-01", "2026-01-02"}, Values: []float64{1 << 30, 2 << 30}},
		{Name: "team <a>", Days: []string{"2026-01-02"}, Values: []float64{5}},
	}
	if err := TrendTo(&b, "dedup_bytes", series); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "<polyline") || !strings.Contains(out, "2.0 GiB") || !strings.Contains(out, "team &lt;a&gt;") {
		t.Fatalf("trend page:\n%s", out)
	}
	b.Reset()
	if err := TrendTo(&b, "manifests", []Series{{Name: "all", Days: []string{"d"}, Values: []float64{0}}}); err != nil {
		t.Fatal(err)
	}
	var many []string
	var vals []float64
	for i := 0; i < 40; i++ {
		many = append(many, time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC).Format(time.DateOnly))
		vals = append(vals, float64(i)+0.5)
	}
	if err := TrendTo(&b, "untagged_pct", []Series{{Name: "all", Days: many, Values: vals}}); err != nil {
		t.Fatal(err)
	}
	if axisLabel("x", 2.25) != "2.2" && axisLabel("x", 2.25) != "2.3" {
		t.Fatalf("axisLabel = %s", axisLabel("x", 2.25))
	}
}

func TestExplain(t *testing.T) {
	t.Parallel()
	created := day
	age := 300
	sup := 40
	ex := &service.Explanation{
		Image: service.ImageInfo{
			Digest: "sha256:" + strings.Repeat("a", 64), Repo: "reg/app", Label: "reg/app:1.0", Tags: []string{"reg/app:1.0"},
			IndexDigest: "sha256:" + strings.Repeat("i", 64), Platform: "linux/amd64", Platforms: []string{"linux/amd64", "linux/arm64"},
			Created: &created, AgeDays: &age, Size: 4096, LayerCount: 2, External: true,
			Labels: map[string]string{"team": "payments", "env": "prod"},
			Config: service.ConfigInfo{Entrypoint: []string{"/bin/sh"}, EnvKeys: []string{"A"}, Ports: []string{"80/tcp"}},
		},
		Lineage: service.LineageInfo{Status: "derived", Generation: 1, Ancestry: chain(),
			Edges: []graph.Edge{{Parent: chain().Nodes[1].ID, Child: chain().Root, Source: "layers", Confidence: "exact"},
				{Parent: "sha256:unknown", Child: chain().Root, Source: "label", Confidence: "declared", Conflict: true}}},
		Staleness: service.StalenessInfo{Verdict: "stale", Reasons: []string{"superseded under reg/app:latest 40 days ago"}, SupersededDays: &sup},
		Layers:    []service.LayerInfo{{Position: 1, Size: 100, SharedBy: 3, CreatedBy: strings.Repeat("x", 100)}, {Position: 2, Size: 50, SharedBy: 1}},
		History:   []model.HistoryEntry{{CreatedBy: "ENV A=1", EmptyLayer: true}, {CreatedBy: "RUN x"}},
		Referrers: []model.Referrer{{Kind: "sbom", Digest: "sha256:" + strings.Repeat("s", 64), ArtifactType: "application/spdx+json"}},
		Findings:  []string{"runs as root"},
	}
	var b bytes.Buffer
	if err := Explain(&b, ex); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"reg/app:1.0", "stale", "(300 days ago)", "linux/amd64, linux/arm64", "external", "superseded under", "parent evidence:",
		"conflicts with layers", "team = payments", "(empty: root)", "shared by 3", "…", "Build history", "Referrers", "! runs as root"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain lacks %q:\n%s", want, out)
		}
	}
	ex.Image.Created = nil
	ex.Staleness = service.StalenessInfo{}
	b.Reset()
	if err := Explain(&b, ex); err != nil || !strings.Contains(b.String(), "unknown (zero timestamp") || !strings.Contains(b.String(), "not computed yet") {
		t.Fatalf("explain without created:\n%s", b.String())
	}
	ex.Staleness = service.StalenessInfo{Verdict: "fresh"}
	b.Reset()
	_ = Explain(&b, ex)
	if !strings.Contains(b.String(), "on its current base") {
		t.Fatalf("fresh explain:\n%s", b.String())
	}
}
