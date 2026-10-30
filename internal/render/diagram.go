package render

import (
	"context"
	"fmt"
	"io"
	"strings"

	"oss.terrastruct.com/d2/d2graph"
	"oss.terrastruct.com/d2/d2layouts/d2dagrelayout"
	"oss.terrastruct.com/d2/d2lib"
	"oss.terrastruct.com/d2/d2renderers/d2svg"
	"oss.terrastruct.com/d2/lib/log"
	"oss.terrastruct.com/d2/lib/textmeasure"

	"github.com/rossedman/strata/internal/graph"
)

// Graph formats.
const (
	FormatTree = "tree"
	Mermaid    = "mermaid"
	Dot        = "dot"
	SVG        = "svg"
	HTML       = "html"
)

// verdictFill is the node fill per verdict, shared by every format.
var verdictFill = map[string]string{
	"fresh":     "#5FD39A",
	"drifting":  "#E8C84A",
	"stale":     "#F08A5D",
	"abandoned": "#A3A3AD",
	"":          "#D9D5EC",
}

// WriteGraph writes a graph in tree, mermaid, dot, svg, html or json.
func WriteGraph(w io.Writer, format string, g *graph.Graph) error {
	switch format {
	case "", FormatTree:
		return Tree(w, g)
	case JSON:
		return JSONTo(w, g)
	case Mermaid:
		return MermaidTo(w, g)
	case Dot:
		return DotTo(w, g)
	case SVG:
		return SVGTo(w, g)
	case HTML:
		return HTMLTo(w, g)
	}
	return fmt.Errorf("unknown graph format %q (tree, json, mermaid, dot, svg, html)", format)
}

// ids assigns short stable identifiers to nodes for text formats.
func ids(g *graph.Graph) map[string]string {
	out := make(map[string]string, len(g.Nodes))
	for i, n := range g.Nodes {
		out[n.ID] = fmt.Sprintf("n%d", i)
	}
	return out
}

// declared reports whether an edge comes only from metadata, which is
// drawn dashed.
func declared(e graph.Edge) bool {
	return e.Source == "label" || e.Source == "annotation"
}

// direction returns whether edges point from child to parent (ancestry)
// and the layout direction.
func direction(g *graph.Graph) (childToParent bool, dir string) {
	if g.Kind == graph.Ancestry {
		return true, "down"
	}
	if g.Kind == graph.Descendants {
		return false, "right"
	}
	return false, "down"
}

// nodeText is the two-line label used by diagram formats.
func nodeText(v View, n graph.Node) string {
	d := v.Display[n.ID]
	if n.Verdict != "" {
		return d.Title + "\n" + d.Sub + " · " + n.Verdict
	}
	return d.Title + "\n" + d.Sub
}

// MermaidTo writes a Mermaid flowchart, which GitHub renders inline.
func MermaidTo(w io.Writer, g *graph.Graph) error {
	id := ids(g)
	v := NewView(g)
	up, dir := direction(g)
	mdir := map[string]string{"down": "TD", "right": "LR"}[dir]
	var b strings.Builder
	fmt.Fprintf(&b, "flowchart %s\n", mdir)
	for v, c := range verdictFill {
		if v != "" {
			fmt.Fprintf(&b, "  classDef %s fill:%s,stroke:#2a2540,color:#1d1a2e\n", v, c)
		}
	}
	b.WriteString("  classDef unknown fill:#D9D5EC,stroke:#2a2540,color:#1d1a2e\n")
	b.WriteString("  classDef phantom fill:#ffffff,stroke:#8E8AA6,stroke-dasharray:4 3,color:#6B6880\n")
	for _, n := range g.Nodes {
		label := mermaidEscape(strings.ReplaceAll(nodeText(v, n), "\n", "<br/>"))
		open, closing := "[\"", "\"]"
		switch n.Kind {
		case graph.KindExternal:
			open, closing = "([\"", "\"])"
		case graph.KindPhantom:
			open, closing = "{{\"", "\"}}"
		case graph.KindCollapsed:
			open, closing = "[[\"", "\"]]"
		case graph.KindOverflow:
			open, closing = ">\"", "\"]"
		}
		class := n.Verdict
		if n.Kind == graph.KindPhantom {
			class = "phantom"
		} else if class == "" {
			class = "unknown"
		}
		fmt.Fprintf(&b, "  %s%s%s%s:::%s\n", id[n.ID], open, label, closing, class)
	}
	var red []int
	for i, e := range g.Edges {
		from, to := id[e.Parent], id[e.Child]
		if up {
			from, to = to, from
		}
		arrow := "-->"
		if declared(e) {
			arrow = "-.->"
		}
		if l := edgeLabel(e); l != "" {
			fmt.Fprintf(&b, "  %s %s|%s| %s\n", from, arrow, mermaidEscape(l), to)
		} else {
			fmt.Fprintf(&b, "  %s %s %s\n", from, arrow, to)
		}
		if e.Conflict {
			red = append(red, i)
		}
	}
	for _, i := range red {
		fmt.Fprintf(&b, "  linkStyle %d stroke:#C8243B,stroke-width:2px\n", i)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// mermaidEscape escapes characters Mermaid treats specially in labels.
func mermaidEscape(s string) string {
	return strings.NewReplacer(`"`, "#quot;").Replace(s)
}

// DotTo writes a Graphviz DOT graph.
func DotTo(w io.Writer, g *graph.Graph) error {
	id := ids(g)
	v := NewView(g)
	up, dir := direction(g)
	var b strings.Builder
	fmt.Fprintf(&b, "digraph strata {\n  rankdir=%s;\n", map[string]string{"down": "TB", "right": "LR"}[dir])
	b.WriteString("  node [shape=box, style=\"rounded,filled\", fontname=\"Helvetica\", fontsize=11];\n  edge [fontname=\"Helvetica\", fontsize=9, color=\"#6B6880\"];\n")
	for _, n := range g.Nodes {
		shape, style := "box", "rounded,filled"
		switch n.Kind {
		case graph.KindExternal:
			shape = "ellipse"
			style = "filled"
		case graph.KindPhantom:
			shape, style = "hexagon", "dashed"
		case graph.KindCollapsed:
			shape = "box3d"
			style = "filled"
		case graph.KindOverflow:
			shape = "note"
		}
		fmt.Fprintf(&b, "  %s [label=%s, shape=%s, style=%q, fillcolor=%q];\n", id[n.ID], dotQuote(nodeText(v, n)), shape, style, verdictFill[n.Verdict])
	}
	for _, e := range g.Edges {
		from, to := id[e.Parent], id[e.Child]
		if up {
			from, to = to, from
		}
		var attrs []string
		if l := edgeLabel(e); l != "" {
			attrs = append(attrs, "label="+dotQuote(l))
		}
		if declared(e) {
			attrs = append(attrs, "style=dashed")
		}
		if e.Conflict {
			attrs = append(attrs, `color="#C8243B"`, "penwidth=2")
		}
		if len(attrs) == 0 {
			fmt.Fprintf(&b, "  %s -> %s;\n", from, to)
		} else {
			fmt.Fprintf(&b, "  %s -> %s [%s];\n", from, to, strings.Join(attrs, ", "))
		}
	}
	b.WriteString("}\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// dotQuote quotes a DOT string, turning newlines into line breaks.
func dotQuote(s string) string {
	s = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
	return `"` + s + `"`
}

// D2Source returns the graph as D2 source text.
func D2Source(g *graph.Graph) string {
	id := ids(g)
	v := NewView(g)
	up, dir := direction(g)
	var b strings.Builder
	fmt.Fprintf(&b, "direction: %s\n", dir)
	for _, n := range g.Nodes {
		fmt.Fprintf(&b, "%s: %s {\n", id[n.ID], d2Quote(nodeText(v, n)))
		shape := "rectangle"
		switch n.Kind {
		case graph.KindExternal:
			shape = "oval"
		case graph.KindPhantom:
			shape = "hexagon"
			b.WriteString("  style.stroke-dash: 4\n")
		case graph.KindCollapsed:
			b.WriteString("  style.multiple: true\n")
		case graph.KindOverflow:
			shape = "page"
		}
		fmt.Fprintf(&b, "  shape: %s\n", shape)
		fill := verdictFill[n.Verdict]
		if n.Kind == graph.KindPhantom {
			fill = "#FFFFFF"
		}
		fmt.Fprintf(&b, "  style.fill: %q\n  style.stroke: \"#2A2540\"\n  style.border-radius: 8\n  style.font-color: \"#14111F\"\n  style.bold: false\n", fill)
		if n.Matched {
			b.WriteString("  style.stroke-width: 3\n  style.stroke: \"#5B47D6\"\n")
		}
		b.WriteString("}\n")
	}
	for _, e := range g.Edges {
		from, to := id[e.Parent], id[e.Child]
		if up {
			from, to = to, from
		}
		if l := edgeLabel(e); l != "" {
			fmt.Fprintf(&b, "%s -> %s: %s {\n", from, to, d2Quote(l))
		} else {
			fmt.Fprintf(&b, "%s -> %s {\n", from, to)
		}
		b.WriteString("  style.stroke: \"#6B6880\"\n  style.font-color: \"#6B6880\"\n")
		if declared(e) {
			b.WriteString("  style.stroke-dash: 5\n")
		}
		if e.Conflict {
			b.WriteString("  style.stroke: \"#C8243B\"\n  style.font-color: \"#C8243B\"\n  style.stroke-width: 3\n")
		}
		b.WriteString("}\n")
	}
	return b.String()
}

// d2Quote quotes a D2 label.
func d2Quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

// SVGTo lays the graph out with D2 (embedded as a library, no Graphviz
// needed) and writes an SVG.
func SVGTo(w io.Writer, g *graph.Graph) error {
	ruler, err := textmeasure.NewRuler()
	if err != nil {
		return err
	}
	resolver := func(string) (d2graph.LayoutGraph, error) { return d2dagrelayout.DefaultLayout, nil }
	ctx := log.WithDefault(context.Background())
	pad := int64(24)
	opts := &d2svg.RenderOpts{Pad: &pad}
	diagram, _, err := d2lib.Compile(ctx, D2Source(g), &d2lib.CompileOptions{LayoutResolver: resolver, Ruler: ruler}, opts)
	if err != nil {
		return fmt.Errorf("lay out graph: %w", err)
	}
	out, err := d2svg.Render(diagram, opts)
	if err != nil {
		return fmt.Errorf("render svg: %w", err)
	}
	_, err = w.Write(out)
	return err
}
