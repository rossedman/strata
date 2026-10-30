package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/rossedman/strata/internal/graph"
	"github.com/rossedman/strata/internal/model"
	"github.com/rossedman/strata/internal/query"
)

// nodes loads graph nodes for ids, keyed by id. Phantom ancestors and
// unknown ids get placeholder nodes.
func (s *Service) nodes(ctx context.Context, ids []string) (map[string]graph.Node, error) {
	out := map[string]graph.Node{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db().QueryContext(ctx, `
		SELECT digest, repo, tag_refs, coalesce(stale, ''), coalesce(stale_reasons::JSON::VARCHAR, '[]'),
			coalesce(strftime(created, '%Y-%m-%d'), ''), coalesce(size, 0), coalesce(layers, 0), platform, external,
			concat_ws(', ',
				CASE WHEN stale = 'abandoned' THEN 'untagged leaf' END,
				CASE WHEN stale_base_drift = 1 THEN 'base drift: 1 gen' WHEN stale_base_drift > 1 THEN 'base drift: ' || stale_base_drift || ' gens' END,
				CASE WHEN stale_superseded_days IS NOT NULL THEN 'superseded ' || stale_superseded_days || 'd ago' END,
				CASE WHEN stale_eol_base THEN 'EOL base' END,
				CASE WHEN stale_label_policy THEN 'label policy' END)
		FROM `+s.view()+` WHERE list_contains(?, digest)`, ids)
	if err != nil {
		return nil, fmt.Errorf("read nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var n graph.Node
		var refs any
		var reasons string
		var external bool
		if err := rows.Scan(&n.ID, &n.Repo, &refs, &n.Verdict, &reasons, &n.Created, &n.Size, &n.Layers, &n.Platform, &external, &n.Note); err != nil {
			return nil, err
		}
		n.Refs = toStrings(refs)
		n.Label = graph.Label(n.Repo, n.ID, n.Refs)
		n.Kind = graph.KindImage
		if external {
			n.Kind = graph.KindExternal
		}
		_ = json.Unmarshal([]byte(reasons), &n.Reasons)
		out[n.ID] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, ok := out[id]; ok {
			continue
		}
		n := graph.Node{ID: id, Kind: graph.KindPhantom, Label: "deleted " + graph.ShortDigest(strings.TrimPrefix(id, model.PhantomPrefix))}
		if !model.IsPhantom(id) {
			n.Label = graph.ShortDigest(id)
		}
		out[id] = n
	}
	return out, nil
}

// build turns ids with depths and edges into a graph, in a stable order.
func (s *Service) build(ctx context.Context, kind, root, title string, depth map[string]int, edges []graph.Edge) (*graph.Graph, error) {
	ids := make([]string, 0, len(depth))
	for id := range depth {
		ids = append(ids, id)
	}
	nodes, err := s.nodes(ctx, ids)
	if err != nil {
		return nil, err
	}
	g := &graph.Graph{Kind: kind, Root: root, Title: title, Edges: edges}
	for _, id := range ids {
		n := nodes[id]
		n.Depth = depth[id]
		g.Nodes = append(g.Nodes, n)
	}
	sort.Slice(g.Nodes, func(i, j int) bool {
		a, b := g.Nodes[i], g.Nodes[j]
		if a.Depth != b.Depth {
			return a.Depth < b.Depth
		}
		if a.Label != b.Label {
			return a.Label < b.Label
		}
		return a.ID < b.ID
	})
	sort.SliceStable(g.Edges, func(i, j int) bool {
		if g.Edges[i].Parent != g.Edges[j].Parent {
			return g.Edges[i].Parent < g.Edges[j].Parent
		}
		return g.Edges[i].Child < g.Edges[j].Child
	})
	return g, nil
}

// Ancestry walks an image's preferred parents back to its root.
func (s *Service) Ancestry(ctx context.Context, ref, platform string) (*graph.Graph, error) {
	digest, err := s.Resolve(ctx, ref, platform)
	if err != nil {
		return nil, err
	}
	return s.ancestryGraph(ctx, digest)
}

// ancestryGraph builds the chain from digest to its root, adding any
// declared parent that conflicts with the inferred one.
func (s *Service) ancestryGraph(ctx context.Context, digest string) (*graph.Graph, error) {
	rows, err := s.db().QueryContext(ctx, `
		WITH RECURSIVE a(id, depth) AS (
			SELECT ?::VARCHAR, 0
			UNION ALL
			SELECT l.parent, a.depth + 1 FROM lineage l JOIN a ON l.digest = a.id
			WHERE l.parent IS NOT NULL AND a.depth < 64
		) SELECT id, min(depth) FROM a GROUP BY id`, digest)
	if err != nil {
		return nil, fmt.Errorf("walk ancestry: %w", err)
	}
	depth := map[string]int{}
	var chain []string
	for rows.Next() {
		var id string
		var d int
		if err := rows.Scan(&id, &d); err != nil {
			_ = rows.Close()
			return nil, err
		}
		depth[id] = d
		chain = append(chain, id)
	}
	_ = rows.Close()
	all, err := s.edges(ctx, chain)
	if err != nil {
		return nil, err
	}
	var edges []graph.Edge
	for _, e := range all {
		if _, ok := depth[e.Parent]; ok && depth[e.Parent] == depth[e.Child]+1 {
			edges = append(edges, e)
			continue
		}
		if e.Conflict {
			// A declared parent off the chain is drawn as a red edge.
			if _, ok := depth[e.Parent]; !ok {
				depth[e.Parent] = depth[e.Child] + 1
			}
			edges = append(edges, e)
		}
	}
	edges = dedupeEdges(edges)
	return s.build(ctx, graph.Ancestry, digest, "ancestry", depth, edges)
}

// dedupeEdges keeps one edge per parent and child, preferring the
// strongest source, but keeps conflicting declared edges.
func dedupeEdges(edges []graph.Edge) []graph.Edge {
	seen := map[[2]string]bool{}
	var out []graph.Edge
	for _, e := range edges {
		k := [2]string{e.Parent, e.Child}
		if seen[k] && !e.Conflict {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

// DescendantOptions control a descendant walk.
type DescendantOptions struct {
	Platform string
	// Depth limits levels below the base; 0 means the whole subtree.
	Depth int
	// AllVersions widens the base to every digest its tag has held.
	AllVersions bool
	// VersionsFrom is a file listing more base digests or refs, one per
	// line.
	VersionsFrom string
	// Budget caps graph nodes; 0 means graph.DefaultBudget.
	Budget int
}

// BaseVersion is one digest a base tag has pointed to, and how much is
// built on it.
type BaseVersion struct {
	Digest      string `json:"digest"`
	Label       string `json:"label"`
	Source      string `json:"source"`
	Confidence  string `json:"confidence"`
	Current     bool   `json:"current"`
	Children    int    `json:"children"`
	Descendants int    `json:"descendants"`
}

// DescendantResult is a descendant graph and the base versions it starts
// from.
type DescendantResult struct {
	Graph    *graph.Graph  `json:"graph"`
	Versions []BaseVersion `json:"versions"`
	// Images counts every descendant before the graph budget applied.
	Images int `json:"images"`
}

// Descendants finds everything built on a base, whatever it is named.
func (s *Service) Descendants(ctx context.Context, ref string, opts DescendantOptions) (*DescendantResult, error) {
	digest, err := s.Resolve(ctx, ref, opts.Platform)
	if err != nil {
		return nil, err
	}
	versions := []BaseVersion{{Digest: digest, Source: "tag", Confidence: model.ConfidenceExact, Current: true}}
	if opts.AllVersions {
		more, err := s.baseVersions(ctx, ref, digest)
		if err != nil {
			return nil, err
		}
		versions = append(versions, more...)
	}
	if opts.VersionsFrom != "" {
		more, err := s.versionsFromFile(ctx, opts.VersionsFrom, opts.Platform)
		if err != nil {
			return nil, err
		}
		versions = append(versions, more...)
	}
	versions = uniqueVersions(versions)
	ids := make([]string, len(versions))
	for i, v := range versions {
		ids[i] = v.Digest
	}
	maxDepth := opts.Depth
	if maxDepth <= 0 {
		maxDepth = 64
	}
	rows, err := s.db().QueryContext(ctx, `
		WITH RECURSIVE d(id, parent, source, depth) AS (
			SELECT unnest(?::VARCHAR[]), NULL::VARCHAR, NULL::VARCHAR, 0
			UNION
			SELECT l.digest, l.parent, l.parent_source, d.depth + 1 FROM lineage l JOIN d ON l.parent = d.id
			WHERE d.depth < ?
		) SELECT id, parent, source, min(depth) FROM d GROUP BY ALL`, ids, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("walk descendants: %w", err)
	}
	depth := map[string]int{}
	var edges []graph.Edge
	for rows.Next() {
		var id string
		var parent, source sql.NullString
		var dep int
		if err := rows.Scan(&id, &parent, &source, &dep); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if old, ok := depth[id]; !ok || dep < old {
			depth[id] = dep
		}
		if parent.Valid {
			edges = append(edges, graph.Edge{Parent: parent.String, Child: id, Source: source.String})
		}
	}
	_ = rows.Close()
	g, err := s.build(ctx, graph.Descendants, digest, "descendants", depth, dedupeEdges(edges))
	if err != nil {
		return nil, err
	}
	res := &DescendantResult{Graph: g, Images: len(depth) - len(versions)}
	children := g.Children()
	for i := range versions {
		v := &versions[i]
		if n := g.Node(v.Digest); n != nil {
			v.Label = n.Label
		}
		v.Children = len(children[v.Digest])
		v.Descendants = countBelow(children, v.Digest)
	}
	res.Versions = versions
	budget := opts.Budget
	if budget <= 0 {
		budget = graph.DefaultBudget
	}
	g.Budget(min(budget, graph.MaxBudget), ids...)
	return res, nil
}

// countBelow counts nodes under id.
func countBelow(children map[string][]string, id string) int {
	n := 0
	for _, c := range children[id] {
		n += 1 + countBelow(children, c)
	}
	return n
}

// baseVersions lists the digests a base tag held before: exactly from
// tag history, and inferred from older images in the base repo created
// before history began.
func (s *Service) baseVersions(ctx context.Context, ref, current string) ([]BaseVersion, error) {
	var repo, os, arch, variant string
	err := s.db().QueryRowContext(ctx, `SELECT repo, os, arch, coalesce(variant, '') FROM images WHERE digest = ?`, current).
		Scan(&repo, &os, &arch, &variant)
	if err != nil {
		return nil, fmt.Errorf("read base: %w", err)
	}
	// The tag the ref names, or every tag that held the current digest.
	rows, err := s.db().QueryContext(ctx, `
		WITH held AS (
			SELECT DISTINCT h.repo, h.tag FROM tag_history h
			LEFT JOIN index_manifests m ON m.index_digest = h.digest
			WHERE (h.repo || ':' || h.tag = ? OR ? LIKE '%/' || split_part(h.repo, '/', -1) || ':' || h.tag
			       OR coalesce(m.digest, h.digest) = ?)
		)
		SELECT DISTINCT coalesce(m.digest, h.digest), 'tag_history', 'exact'
		FROM tag_history h JOIN held USING (repo, tag)
		LEFT JOIN index_manifests m ON m.index_digest = h.digest AND m.os = ? AND m.arch = ? AND coalesce(m.variant, '') = ?
		WHERE h.digest IS NOT NULL AND coalesce(m.digest, h.digest) IN (SELECT digest FROM images)
		UNION
		SELECT i.digest, 'repo', 'inferred' FROM images i
		WHERE i.repo = ? AND i.os = ? AND i.arch = ? AND coalesce(i.variant, '') = ?
		  AND i.created < coalesce((SELECT min(seen_at) FROM tag_history WHERE repo = ?), '9999-01-01'::TIMESTAMPTZ)
		  AND i.digest NOT IN (SELECT coalesce(m.digest, h.digest) FROM tag_history h
		                       LEFT JOIN index_manifests m ON m.index_digest = h.digest WHERE h.digest IS NOT NULL)`,
		ref, ref, current, os, arch, variant, repo, os, arch, variant, repo)
	if err != nil {
		return nil, fmt.Errorf("read base versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []BaseVersion
	for rows.Next() {
		var v BaseVersion
		if err := rows.Scan(&v.Digest, &v.Source, &v.Confidence); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// versionsFromFile reads base digests or refs, one per line, ignoring
// blanks and # comments.
func (s *Service) versionsFromFile(ctx context.Context, file, platform string) ([]BaseVersion, error) {
	data, err := os.ReadFile(file) // #nosec G304 -- the user names their own list.
	if err != nil {
		return nil, fmt.Errorf("read versions: %w", err)
	}
	var out []BaseVersion
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		d, err := s.Resolve(ctx, line, platform)
		if err != nil {
			return nil, fmt.Errorf("versions file: %w", err)
		}
		out = append(out, BaseVersion{Digest: d, Source: "file", Confidence: model.ConfidenceDeclared})
	}
	return out, nil
}

// uniqueVersions drops repeated digests, keeping the first.
func uniqueVersions(vs []BaseVersion) []BaseVersion {
	seen := map[string]bool{}
	var out []BaseVersion
	for _, v := range vs {
		if !seen[v.Digest] {
			seen[v.Digest] = true
			out = append(out, v)
		}
	}
	return out
}

// QueryGraph draws the images a filter matches plus their ancestry up to
// shared roots.
func (s *Service) QueryGraph(ctx context.Context, filter string, budget int) (*graph.Graph, error) {
	c, err := query.Compile(filter)
	if err != nil {
		return nil, input(err)
	}
	if budget <= 0 {
		budget = graph.DefaultBudget
	}
	budget = min(budget, graph.MaxBudget)
	matched, err := s.strings(ctx, `SELECT digest FROM `+s.view()+` WHERE `+c.Where+` ORDER BY digest LIMIT `+fmt.Sprint(graph.MaxBudget), c.Args...)
	if err != nil {
		return nil, fmt.Errorf("run filter: %w", err)
	}
	rows, err := s.db().QueryContext(ctx, `
		WITH RECURSIVE a(id, parent, source) AS (
			SELECT l.digest, l.parent, l.parent_source FROM lineage l WHERE list_contains(?, l.digest)
			UNION
			SELECT l.digest, l.parent, l.parent_source FROM lineage l JOIN a ON l.digest = a.parent
		) SELECT id, parent, source, (SELECT coalesce(generation, 0) FROM lineage WHERE digest = id) FROM a`, matched)
	if err != nil {
		return nil, fmt.Errorf("walk query ancestry: %w", err)
	}
	depth := map[string]int{}
	var edges []graph.Edge
	for rows.Next() {
		var id string
		var parent, source sql.NullString
		var gen int
		if err := rows.Scan(&id, &parent, &source, &gen); err != nil {
			_ = rows.Close()
			return nil, err
		}
		depth[id] = gen
		if parent.Valid {
			edges = append(edges, graph.Edge{Parent: parent.String, Child: id, Source: source.String})
		}
	}
	_ = rows.Close()
	g, err := s.build(ctx, graph.Query, "", filter, depth, dedupeEdges(edges))
	if err != nil {
		return nil, err
	}
	isMatch := map[string]bool{}
	for _, m := range matched {
		isMatch[m] = true
	}
	for i := range g.Nodes {
		g.Nodes[i].Matched = isMatch[g.Nodes[i].ID]
	}
	g.Budget(budget)
	return g, nil
}
