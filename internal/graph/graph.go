// Package graph is the lineage graph strata renders: nodes for images,
// external roots and phantom ancestors, edges with their provenance, and
// the budget rules that keep a descendant tree of a popular base readable.
package graph

import (
	"fmt"
	"sort"
	"strings"
)

// Node kinds.
const (
	KindImage     = "image"
	KindExternal  = "external"
	KindPhantom   = "phantom"
	KindCollapsed = "collapsed"
	KindOverflow  = "overflow"
)

// Graph kinds.
const (
	Ancestry    = "ancestry"
	Descendants = "descendants"
	Query       = "query"
)

// DefaultBudget is the node budget for rendered graphs.
const DefaultBudget = 500

// MaxBudget is the hard cap on --budget, which keeps HTML output openable.
const MaxBudget = 20000

// Node is one vertex.
type Node struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Repo    string   `json:"repo,omitempty"`
	Refs    []string `json:"refs,omitempty"`
	Kind    string   `json:"kind"`
	Verdict string   `json:"verdict,omitempty"`
	Reasons []string `json:"reasons,omitempty"`
	// Note is a compact summary of the staleness signals, such as
	// "base drift: 3 gens".
	Note     string   `json:"note,omitempty"`
	Created  string   `json:"created,omitempty"`
	Size     int64    `json:"size,omitempty"`
	Layers   int      `json:"layers,omitempty"`
	Depth    int      `json:"depth"`
	Matched  bool     `json:"matched,omitempty"`
	Count    int      `json:"count,omitempty"`
	Members  []string `json:"members,omitempty"`
	Platform string   `json:"platform,omitempty"`
}

// Edge links a parent to a child.
type Edge struct {
	Parent     string `json:"parent"`
	Child      string `json:"child"`
	Source     string `json:"source"`
	Confidence string `json:"confidence,omitempty"`
	Conflict   bool   `json:"conflict,omitempty"`
}

// Graph is a set of nodes and edges of one kind.
type Graph struct {
	Kind   string `json:"kind"`
	Root   string `json:"root,omitempty"`
	Title  string `json:"title"`
	Nodes  []Node `json:"nodes"`
	Edges  []Edge `json:"edges"`
	Hidden int    `json:"hidden,omitempty"`
}

// Node returns the node with an ID, or nil.
func (g *Graph) Node(id string) *Node {
	for i := range g.Nodes {
		if g.Nodes[i].ID == id {
			return &g.Nodes[i]
		}
	}
	return nil
}

// Children returns each node's distinct children, sorted by label then
// ID, with overflow nodes last.
func (g *Graph) Children() map[string][]string {
	labels := map[string]string{}
	overflow := map[string]bool{}
	for _, n := range g.Nodes {
		labels[n.ID] = n.Label
		overflow[n.ID] = n.Kind == KindOverflow
	}
	out := map[string][]string{}
	seen := map[[2]string]bool{}
	for _, e := range g.Edges {
		if k := [2]string{e.Parent, e.Child}; !seen[k] {
			seen[k] = true
			out[e.Parent] = append(out[e.Parent], e.Child)
		}
	}
	for _, kids := range out {
		sort.Slice(kids, func(i, j int) bool {
			if overflow[kids[i]] != overflow[kids[j]] {
				return overflow[kids[j]]
			}
			if labels[kids[i]] != labels[kids[j]] {
				return labels[kids[i]] < labels[kids[j]]
			}
			return kids[i] < kids[j]
		})
	}
	return out
}

// ShortDigest abbreviates sha256:<hex> to sha256:<first 12 hex>.
func ShortDigest(d string) string {
	if len(d) > 19 && strings.HasPrefix(d, "sha256:") {
		return d[:19]
	}
	return d
}

// Label picks the most readable name for an image: its first tag ref
// that is not latest, then latest, then repo@short digest.
func Label(repo, digest string, tagRefs []string) string {
	refs := append([]string{}, tagRefs...)
	sort.Strings(refs)
	for _, r := range refs {
		if !strings.HasSuffix(r, ":latest") {
			return r
		}
	}
	if len(refs) > 0 {
		return refs[0]
	}
	if repo == "" {
		return ShortDigest(digest)
	}
	return repo + "@" + ShortDigest(digest)
}

// Budget keeps a graph within a node budget. Children of one parent that
// share a repo collapse into one node first; if the graph is still too
// big, the deepest nodes are cut and each parent that lost children gets
// a "+N more" node. Nodes listed in keep are never collapsed or cut.
func (g *Graph) Budget(budget int, keep ...string) {
	if budget <= 0 || len(g.Nodes) <= budget {
		return
	}
	g.collapseSiblings(keep)
	if len(g.Nodes) <= budget {
		return
	}
	g.cut(budget, keep)
}

// collapseSiblings merges children of one parent that share a repo.
func (g *Graph) collapseSiblings(keep []string) {
	kept := set(keep)
	byID := map[string]*Node{}
	for i := range g.Nodes {
		byID[g.Nodes[i].ID] = &g.Nodes[i]
	}
	hasKids := map[string]bool{}
	for _, e := range g.Edges {
		hasKids[e.Parent] = true
	}
	type key struct{ parent, repo string }
	groups := map[key][]string{}
	for _, e := range g.Edges {
		n := byID[e.Child]
		// Only leaves collapse, so no subtree is lost.
		if n == nil || n.Kind != KindImage || hasKids[n.ID] || kept[n.ID] {
			continue
		}
		groups[key{e.Parent, n.Repo}] = append(groups[key{e.Parent, n.Repo}], n.ID)
	}
	remove := map[string]bool{}
	var added []Node
	var addedEdges []Edge
	for k, ids := range groups {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		c := Node{
			ID:    "collapsed:" + k.parent + ":" + k.repo,
			Label: fmt.Sprintf("%s ×%d", k.repo, len(ids)),
			Repo:  k.repo, Kind: KindCollapsed, Count: len(ids),
			Depth: byID[ids[0]].Depth,
		}
		verdicts := map[string]int{}
		for _, id := range ids {
			remove[id] = true
			c.Members = append(c.Members, byID[id].Label)
			verdicts[byID[id].Verdict]++
		}
		c.Verdict = worstVerdict(verdicts)
		added = append(added, c)
		addedEdges = append(addedEdges, Edge{Parent: k.parent, Child: c.ID, Source: "layers"})
	}
	g.drop(remove)
	sort.Slice(added, func(i, j int) bool { return added[i].ID < added[j].ID })
	g.Nodes = append(g.Nodes, added...)
	g.Edges = append(g.Edges, addedEdges...)
}

// cut keeps the shallowest nodes up to the budget and adds overflow nodes.
func (g *Graph) cut(budget int, keep []string) {
	kept := set(keep)
	order := make([]int, len(g.Nodes))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		na, nb := g.Nodes[order[a]], g.Nodes[order[b]]
		if kept[na.ID] != kept[nb.ID] {
			return kept[na.ID]
		}
		if na.Depth != nb.Depth {
			return na.Depth < nb.Depth
		}
		return na.Label < nb.Label
	})
	// Keep as many nodes as fit once each surviving parent that lost
	// children gets its overflow node.
	parentOf := map[string]string{}
	for _, e := range g.Edges {
		parentOf[e.Child] = e.Parent
	}
	removedFor := func(k int) map[string]bool {
		remove := map[string]bool{}
		for _, i := range order[min(k, len(order)):] {
			remove[g.Nodes[i].ID] = true
		}
		return remove
	}
	overflows := func(remove map[string]bool) int {
		ps := map[string]bool{}
		for id := range remove {
			if p, ok := parentOf[id]; ok && !remove[p] {
				ps[p] = true
			}
		}
		return len(ps)
	}
	k := budget
	for i := 0; i < 32; i++ {
		next := max(1, budget-overflows(removedFor(k)))
		if next == k {
			break
		}
		k = next
	}
	for k > 1 && k+overflows(removedFor(k)) > budget {
		k--
	}
	remove := removedFor(k)
	kids := map[string][]string{}
	for _, e := range g.Edges {
		kids[e.Parent] = append(kids[e.Parent], e.Child)
	}
	count := map[string]int{}
	for _, n := range g.Nodes {
		count[n.ID] = max(1, n.Count)
	}
	lost := map[string]int{}
	for _, e := range g.Edges {
		if remove[e.Child] && !remove[e.Parent] {
			lost[e.Parent] += weight(e.Child, kids, count, remove, map[string]bool{})
		}
	}
	hidden := 0
	for _, n := range g.Nodes {
		if remove[n.ID] {
			hidden += max(1, n.Count)
		}
	}
	g.drop(remove)
	parents := make([]string, 0, len(lost))
	for p := range lost {
		parents = append(parents, p)
	}
	sort.Strings(parents)
	for _, p := range parents {
		id := "overflow:" + p
		depth := 0
		if n := g.Node(p); n != nil {
			depth = n.Depth + 1
		}
		g.Nodes = append(g.Nodes, Node{ID: id, Label: fmt.Sprintf("+%d more", lost[p]), Kind: KindOverflow, Count: lost[p], Depth: depth})
		g.Edges = append(g.Edges, Edge{Parent: p, Child: id, Source: "layers"})
	}
	g.Hidden += hidden
}

// weight counts the images a removed node stands for, including its
// removed descendants.
func weight(id string, kids map[string][]string, count map[string]int, removed, seen map[string]bool) int {
	if seen[id] {
		return 0
	}
	seen[id] = true
	n := count[id]
	for _, k := range kids[id] {
		if removed[k] {
			n += weight(k, kids, count, removed, seen)
		}
	}
	return n
}

// drop removes nodes and every edge touching them.
func (g *Graph) drop(remove map[string]bool) {
	nodes := g.Nodes[:0]
	for _, n := range g.Nodes {
		if !remove[n.ID] {
			nodes = append(nodes, n)
		}
	}
	g.Nodes = nodes
	edges := g.Edges[:0]
	for _, e := range g.Edges {
		if !remove[e.Parent] && !remove[e.Child] {
			edges = append(edges, e)
		}
	}
	g.Edges = edges
}

// worstVerdict returns the most severe verdict in a count map.
func worstVerdict(v map[string]int) string {
	for _, k := range []string{"abandoned", "stale", "drifting", "fresh"} {
		if v[k] > 0 {
			return k
		}
	}
	return ""
}

// set builds a lookup set.
func set(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}
