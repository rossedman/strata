package render

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/rossedman/strata/internal/graph"
	"github.com/rossedman/strata/internal/ui"
)

// Tree writes a graph as an indented tree with aligned columns: name,
// short digest, then the verdict for the first node and edge provenance
// for the rest. Ancestry trees start at the image and descend to the
// root; descendant trees start at the base.
func Tree(w io.Writer, g *graph.Graph) error {
	kids := map[string][]string{}
	var roots []string
	if g.Kind == graph.Ancestry {
		// Walk from the image to its root: each node's "child" in the tree
		// is its parent in the graph.
		seen := map[[2]string]bool{}
		for _, e := range g.Edges {
			if k := [2]string{e.Child, e.Parent}; !e.Conflict && !seen[k] {
				seen[k] = true
				kids[e.Child] = append(kids[e.Child], e.Parent)
			}
		}
		roots = []string{g.Root}
	} else {
		kids = g.Children()
		hasParent := map[string]bool{}
		for _, e := range g.Edges {
			hasParent[e.Child] = true
		}
		for _, n := range g.Nodes {
			if !hasParent[n.ID] {
				roots = append(roots, n.ID)
			}
		}
	}
	edgeInfo := map[[2]string][]string{}
	conflicts := map[string][]string{}
	for _, e := range g.Edges {
		k := [2]string{e.Parent, e.Child}
		edgeInfo[k] = append(edgeInfo[k], e.Source)
		if e.Conflict {
			conflicts[e.Child] = append(conflicts[e.Child], e.Parent)
		}
	}
	type line struct {
		prefix, label, digest, note string
		verdict                     string
	}
	var lines []line
	seen := map[string]bool{}
	var walk func(id, prefix, childPrefix, from string, first bool)
	walk = func(id, prefix, childPrefix, from string, first bool) {
		if seen[id] {
			return
		}
		seen[id] = true
		n := g.Node(id)
		if n == nil {
			return
		}
		l := line{prefix: prefix, label: n.Label, digest: graph.ShortDigest(strings.TrimPrefix(n.ID, "phantom:"))}
		switch n.Kind {
		case graph.KindCollapsed, graph.KindOverflow:
			l.digest = ""
		}
		var notes []string
		if first || g.Kind != graph.Ancestry {
			if n.Verdict != "" {
				l.verdict = n.Verdict
				notes = append(notes, verdictNote(n))
			}
		}
		if from != "" {
			k := [2]string{from, id}
			if g.Kind == graph.Ancestry {
				k = [2]string{id, from}
			}
			if src := uniqueSorted(edgeInfo[k]); len(src) > 0 && g.Kind == graph.Ancestry {
				notes = append(notes, strings.Join(src, " + "))
			}
		}
		switch n.Kind {
		case graph.KindExternal:
			notes = append(notes, "(external)")
		case graph.KindPhantom:
			notes = append(notes, "(not in registry)")
		}
		for _, p := range conflicts[id] {
			pn := g.Node(p)
			name := p
			if pn != nil {
				name = pn.Label
			}
			notes = append(notes, ui.Bad.Render("conflict: declared "+name))
		}
		l.note = strings.Join(notes, "  ")
		lines = append(lines, l)
		cs := kids[id]
		for i, c := range cs {
			last := i == len(cs)-1
			branch, next := "├─ ", "│  "
			if last {
				branch, next = "└─ ", "   "
			}
			walk(c, childPrefix+branch, childPrefix+next, id, false)
		}
	}
	for i, r := range roots {
		if i > 0 {
			lines = append(lines, line{})
		}
		walk(r, "", "", "", true)
	}
	width := 0
	for _, l := range lines {
		width = max(width, lipgloss.Width(l.prefix+l.label))
	}
	var b strings.Builder
	for _, l := range lines {
		if l.label == "" {
			b.WriteString("\n")
			continue
		}
		pad := strings.Repeat(" ", width-lipgloss.Width(l.prefix+l.label)+2)
		b.WriteString(ui.Branch.Render(l.prefix) + ui.Label.Render(l.label) + pad + ui.Dim.Render(fmt.Sprintf("%-20s", l.digest)))
		if l.note != "" {
			if l.verdict != "" {
				b.WriteString(ui.VerdictStyle(l.verdict).Render(l.note))
			} else {
				b.WriteString(ui.Dim.Render(l.note))
			}
		}
		b.WriteString("\n")
	}
	if g.Hidden > 0 {
		b.WriteString(ui.Dim.Render(fmt.Sprintf("(%d images hidden by the node budget; raise it with --budget)", g.Hidden)) + "\n")
	}
	ui.Fprint(w, b.String())
	return nil
}

// verdictNote summarizes a node's verdict and its signals.
func verdictNote(n *graph.Node) string {
	if n.Note == "" {
		return n.Verdict
	}
	return n.Verdict + " (" + n.Note + ")"
}

// uniqueSorted returns the distinct values of xs, in source-strength
// order.
func uniqueSorted(xs []string) []string {
	rank := map[string]int{"layers": 0, "attestation": 1, "annotation": 2, "label": 3}
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return rank[out[i]] < rank[out[j]] })
	return out
}
