package render

import (
	"strings"

	"github.com/rossedman/strata/internal/graph"
)

// Display is how a node is named in visual formats: a title, a subtitle
// with its short digest and status, and the registry host, which is left
// out of the title when every node shares it.
type Display struct {
	Title string `json:"title"`
	Sub   string `json:"sub"`
}

// View is the data a visual format draws: the graph plus each node's
// display text and the shared registry host, if any.
type View struct {
	Graph   *graph.Graph       `json:"graph"`
	Host    string             `json:"host"`
	Display map[string]Display `json:"display"`
}

// NewView names every node for display.
func NewView(g *graph.Graph) View {
	host := sharedHost(g)
	v := View{Graph: g, Host: host, Display: make(map[string]Display, len(g.Nodes))}
	for _, n := range g.Nodes {
		v.Display[n.ID] = display(n, host)
	}
	return v
}

// hostOf returns a repo's registry host, or "" when the first path
// element does not look like one.
func hostOf(repo string) string {
	h, _, ok := strings.Cut(repo, "/")
	if !ok || (!strings.ContainsAny(h, ".:") && h != "localhost") {
		return ""
	}
	return h
}

// sharedHost returns the host every image node's repo is on, or "".
func sharedHost(g *graph.Graph) string {
	host := ""
	for _, n := range g.Nodes {
		if n.Repo == "" {
			continue
		}
		h := hostOf(n.Repo)
		if h == "" || (host != "" && h != host) {
			return ""
		}
		host = h
	}
	return host
}

// strip removes the shared host from a name.
func strip(s, host string) string {
	if host == "" {
		return s
	}
	return strings.TrimPrefix(s, host+"/")
}

// display names one node.
func display(n graph.Node, host string) Display {
	short := graph.ShortDigest(strings.TrimPrefix(n.ID, "phantom:"))
	switch n.Kind {
	case graph.KindCollapsed:
		return Display{Title: strip(n.Label, host), Sub: "collapsed siblings"}
	case graph.KindOverflow:
		return Display{Title: n.Label, Sub: "over the node budget"}
	case graph.KindPhantom:
		return Display{Title: "not in registry", Sub: short + " · shared layers"}
	}
	title := strip(n.Label, host)
	sub := short
	if repo, _, ok := strings.Cut(title, "@sha256:"); ok {
		title = repo
		sub = short + " · untagged"
	}
	if n.Kind == graph.KindExternal {
		sub += " · external"
	}
	return Display{Title: title, Sub: sub}
}

// edgeLabel is the text drawn on an edge: nothing for the common case of
// a parent found from layers, the source for declared parents, and a
// warning for conflicts.
func edgeLabel(e graph.Edge) string {
	switch {
	case e.Conflict:
		return "conflict: " + e.Source
	case e.Source == "layers" || e.Source == "":
		return ""
	}
	return e.Source
}
