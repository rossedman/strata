package graph

import (
	"fmt"
	"strings"
	"testing"
)

func TestLabelAndShortDigest(t *testing.T) {
	t.Parallel()
	d := "sha256:" + strings.Repeat("a", 64)
	tests := []struct {
		repo string
		tags []string
		want string
	}{
		{"r", []string{"r:latest", "r:1.2"}, "r:1.2"},
		{"r", []string{"r:latest"}, "r:latest"},
		{"r", nil, "r@sha256:aaaaaaaaaaaa"},
		{"", nil, "sha256:aaaaaaaaaaaa"},
	}
	for _, tt := range tests {
		if got := Label(tt.repo, d, tt.tags); got != tt.want {
			t.Errorf("Label(%v) = %s, want %s", tt.tags, got, tt.want)
		}
	}
	if ShortDigest("short") != "short" {
		t.Fatal("ShortDigest changed a short string")
	}
}

// fan builds a base with n leaf children in repo, plus one child with a
// grandchild.
func fan(n int, repo string) *Graph {
	g := &Graph{Kind: Descendants, Root: "base", Nodes: []Node{{ID: "base", Label: "base", Kind: KindImage}}}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%03d", i)
		verdict := "fresh"
		if i == 0 {
			verdict = "stale"
		}
		g.Nodes = append(g.Nodes, Node{ID: id, Label: id, Repo: repo, Kind: KindImage, Depth: 1, Verdict: verdict})
		g.Edges = append(g.Edges, Edge{Parent: "base", Child: id, Source: "layers"})
	}
	g.Nodes = append(g.Nodes, Node{ID: "mid", Label: "mid", Repo: "other", Kind: KindImage, Depth: 1},
		Node{ID: "leaf", Label: "leaf", Repo: "other", Kind: KindImage, Depth: 2})
	g.Edges = append(g.Edges, Edge{Parent: "base", Child: "mid"}, Edge{Parent: "mid", Child: "leaf"})
	return g
}

func TestBudgetCollapsesSiblings(t *testing.T) {
	t.Parallel()
	g := fan(50, "team/app")
	g.Budget(10)
	if len(g.Nodes) != 4 {
		t.Fatalf("nodes after collapse = %d: %+v", len(g.Nodes), g.Nodes)
	}
	c := g.Node("collapsed:base:team/app")
	if c == nil || c.Count != 50 || c.Label != "team/app ×50" || c.Verdict != "stale" || len(c.Members) != 50 {
		t.Fatalf("collapsed node = %+v", c)
	}
	if len(g.Children()["base"]) != 2 {
		t.Fatalf("base children = %v", g.Children()["base"])
	}
}

func TestBudgetCuts(t *testing.T) {
	t.Parallel()
	g := &Graph{Nodes: []Node{{ID: "base", Label: "base", Kind: KindImage}}}
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("c%02d", i)
		g.Nodes = append(g.Nodes, Node{ID: id, Label: id, Repo: id, Kind: KindImage, Depth: 1})
		g.Edges = append(g.Edges, Edge{Parent: "base", Child: id})
		gc := id + "-child"
		g.Nodes = append(g.Nodes, Node{ID: gc, Label: gc, Repo: gc, Kind: KindImage, Depth: 2})
		g.Edges = append(g.Edges, Edge{Parent: id, Child: gc})
	}
	g.Budget(10, "c29-child")
	if len(g.Nodes) > 12 {
		t.Fatalf("nodes after cut = %d", len(g.Nodes))
	}
	if g.Node("c29-child") == nil {
		t.Fatal("kept node was cut")
	}
	var overflow *Node
	for i := range g.Nodes {
		if g.Nodes[i].Kind == KindOverflow {
			overflow = &g.Nodes[i]
		}
	}
	if overflow == nil || g.Hidden == 0 || !strings.HasPrefix(overflow.Label, "+") {
		t.Fatalf("overflow = %+v hidden = %d", overflow, g.Hidden)
	}
	for _, e := range g.Edges {
		if g.Node(e.Parent) == nil || g.Node(e.Child) == nil {
			t.Fatalf("dangling edge %+v", e)
		}
	}
	small := fan(3, "r")
	small.Budget(100)
	if len(small.Nodes) != 6 {
		t.Fatal("graph under budget changed")
	}
	small.Budget(0)
	if len(small.Nodes) != 6 {
		t.Fatal("zero budget changed the graph")
	}
	if worstVerdict(map[string]int{}) != "" {
		t.Fatal("worstVerdict of nothing")
	}
}

func BenchmarkBudget(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		g := fan(5000, "team/app")
		for i := 0; i < 2000; i++ {
			id := fmt.Sprintf("x%04d", i)
			g.Nodes = append(g.Nodes, Node{ID: id, Label: id, Repo: id, Kind: KindImage, Depth: 2})
			g.Edges = append(g.Edges, Edge{Parent: "mid", Child: id})
		}
		b.StartTimer()
		g.Budget(DefaultBudget)
	}
}

func FuzzBudget(f *testing.F) {
	f.Add(uint8(20), uint8(5), uint8(3))
	f.Add(uint8(200), uint8(1), uint8(50))
	f.Fuzz(func(t *testing.T, n, repos, budget uint8) {
		g := &Graph{Nodes: []Node{{ID: "root", Label: "root", Kind: KindImage}}}
		for i := 0; i < int(n); i++ {
			id := fmt.Sprintf("n%d", i)
			parent := "root"
			if i > 3 && i%3 == 0 {
				parent = fmt.Sprintf("n%d", i/3)
			}
			repo := fmt.Sprintf("r%d", i%(int(repos)+1))
			g.Nodes = append(g.Nodes, Node{ID: id, Label: id, Repo: repo, Kind: KindImage, Depth: 1 + i%3})
			g.Edges = append(g.Edges, Edge{Parent: parent, Child: id})
		}
		images := len(g.Nodes)
		g.Budget(int(budget))
		// Every edge points at nodes that still exist, the root survives,
		// and no image is lost: kept nodes plus counted ones add up.
		if g.Node("root") == nil {
			t.Fatal("root was cut")
		}
		for _, e := range g.Edges {
			if g.Node(e.Parent) == nil || g.Node(e.Child) == nil {
				t.Fatalf("dangling edge %+v", e)
			}
		}
		total := 0
		for _, nd := range g.Nodes {
			total += max(1, nd.Count)
		}
		if budget >= 2 && images > int(budget) && len(g.Nodes) > int(budget) {
			t.Fatalf("%d nodes exceed the budget of %d", len(g.Nodes), budget)
		}
		if budget > 0 && images > int(budget) && total < images-g.Hidden {
			t.Fatalf("lost images: %d shown + %d hidden < %d", total, g.Hidden, images)
		}
	})
}
