package service

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rossedman/strata/internal/graph"
	"github.com/rossedman/strata/internal/model"
	"github.com/rossedman/strata/internal/query"
)

// subtreeStats recomputes, for every lineage node, the images and verdict
// counts of its subtree, so outline rows cost one lookup however large the
// subtree is.
func (s *Service) subtreeStats(ctx context.Context) error {
	tx, err := s.db().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmts := []string{
		`CREATE OR REPLACE TEMP TABLE new_subtree_stats AS
			WITH RECURSIVE up(id, anc, d) AS (
				SELECT digest, digest, 0 FROM lineage
				UNION ALL
				SELECT up.id, l.parent, up.d + 1 FROM up JOIN lineage l ON l.digest = up.anc
				WHERE l.parent IS NOT NULL AND up.d < 64
			)
			SELECT up.anc AS id, count(i.digest) AS images,
				count(*) FILTER (WHERE s.verdict = 'fresh') AS fresh,
				count(*) FILTER (WHERE s.verdict = 'drifting') AS drifting,
				count(*) FILTER (WHERE s.verdict = 'stale') AS stale,
				count(*) FILTER (WHERE s.verdict = 'abandoned') AS abandoned,
				max(i.created) FILTER (WHERE year(i.created) >= 1980) AS newest
			FROM up LEFT JOIN images i ON i.digest = up.id LEFT JOIN staleness s ON s.digest = up.id
			GROUP BY up.anc`,
		// Only nodes whose counts moved are rewritten.
		`CREATE OR REPLACE TEMP TABLE subtree_changed AS
			SELECT id FROM (SELECT * FROM subtree_stats EXCEPT SELECT * FROM new_subtree_stats)
			UNION SELECT id FROM (SELECT * FROM new_subtree_stats EXCEPT SELECT * FROM subtree_stats)`,
		`DELETE FROM subtree_stats WHERE id IN (SELECT id FROM subtree_changed)`,
		`INSERT INTO subtree_stats SELECT * FROM new_subtree_stats WHERE id IN (SELECT id FROM subtree_changed)`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("subtree stats: %w", err)
		}
	}
	return tx.Commit()
}

// VerdictCounts are images per verdict.
type VerdictCounts struct {
	Fresh     int64 `json:"fresh"`
	Drifting  int64 `json:"drifting"`
	Stale     int64 `json:"stale"`
	Abandoned int64 `json:"abandoned"`
}

// OutlineRow is one row of the lineage outline: a base, a repo group or an
// image.
type OutlineRow struct {
	ID          string        `json:"id"`
	Parent      string        `json:"parent,omitempty"`
	Kind        string        `json:"kind"`
	Name        string        `json:"name"`
	Repo        string        `json:"repo,omitempty"`
	Team        string        `json:"team,omitempty"`
	Digest      string        `json:"digest,omitempty"`
	Tags        []string      `json:"tags,omitempty"`
	Verdict     string        `json:"verdict,omitempty"`
	Images      int64         `json:"images"`
	Verdicts    VerdictCounts `json:"verdicts"`
	Drift       int           `json:"drift,omitempty"`
	Newest      *time.Time    `json:"newest,omitempty"`
	HasChildren bool          `json:"has_children"`
	Depth       int           `json:"depth"`
}

// Outline is one page of an outline row's children.
type Outline struct {
	Parent string       `json:"parent,omitempty"`
	Rows   []OutlineRow `json:"rows"`
	Total  int64        `json:"total"`
	Offset int          `json:"offset"`
}

// Outline row kinds and ids.
const (
	// Unrooted is the pseudo-root holding images with no known parent and
	// nothing built on them.
	Unrooted = "unrooted"
	// repoPrefix starts the id of a repo group row: repo|<parent>|<repo>.
	repoPrefix = "repo|"
)

// Outline returns one page of the children of an outline row: root bases
// when parent is empty, then bases and repo groups, then a repo group's
// images.
func (s *Service) Outline(ctx context.Context, parent string, onlyStale bool, offset, limit int) (*Outline, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	offset = max(0, offset)
	out := &Outline{Parent: parent, Offset: offset, Rows: []OutlineRow{}}
	var err error
	switch {
	case parent == "":
		err = s.outlineRoots(ctx, out, onlyStale, offset, limit)
	case strings.HasPrefix(parent, repoPrefix):
		rest := strings.TrimPrefix(parent, repoPrefix)
		p, repo, ok := strings.Cut(rest, "|")
		if !ok {
			return nil, input(fmt.Errorf("bad outline id %q", parent))
		}
		err = s.outlineImages(ctx, out, p, repo, onlyStale, offset, limit)
	default:
		err = s.outlineChildren(ctx, out, parent, onlyStale, offset, limit)
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// staleFilter is the SQL condition for "only stale" over a row's counts.
func staleFilter(onlyStale bool) string {
	if onlyStale {
		return "(stale + abandoned) > 0"
	}
	return "true"
}

// outlineRoots lists root bases, plus the unrooted pseudo-root when lone
// images exist.
func (s *Service) outlineRoots(ctx context.Context, out *Outline, onlyStale bool, offset, limit int) error {
	q := `
		WITH rows AS (
			SELECT l.digest AS id, 'node' AS t, NULL::VARCHAR AS repo, st.images, st.fresh, st.drifting, st.stale, st.abandoned, st.newest
			FROM lineage l JOIN subtree_stats st ON st.id = l.digest
			WHERE l.parent IS NULL AND l.descendants > 0
			UNION ALL
			SELECT '` + Unrooted + `', 'unrooted', NULL, count(*),
				count(*) FILTER (WHERE s.verdict = 'fresh'), count(*) FILTER (WHERE s.verdict = 'drifting'),
				count(*) FILTER (WHERE s.verdict = 'stale'), count(*) FILTER (WHERE s.verdict = 'abandoned'),
				max(i.created) FILTER (WHERE year(i.created) >= 1980)
			FROM lineage l JOIN images i ON i.digest = l.digest LEFT JOIN staleness s ON s.digest = l.digest
			WHERE l.parent IS NULL AND l.descendants = 0
			HAVING count(*) > 0
		)
		SELECT *, count(*) OVER () FROM rows WHERE ` + staleFilter(onlyStale) + `
		ORDER BY t = 'unrooted', images DESC, id LIMIT ? OFFSET ?`
	return s.fillRows(ctx, out, "", 0, q, limit, offset)
}

// outlineChildren lists a node's children: those with descendants as
// base rows, leaf images grouped into one row per repo.
func (s *Service) outlineChildren(ctx context.Context, out *Outline, parent string, onlyStale bool, offset, limit int) error {
	cond := "l.parent = ?"
	args := []any{parent}
	if parent == Unrooted {
		cond, args = "l.parent IS NULL", nil
	}
	q := `
		WITH kids AS (
			SELECT l.digest, l.descendants FROM lineage l WHERE ` + cond + `
		),
		rows AS (
			SELECT k.digest AS id, 'node' AS t, NULL::VARCHAR AS repo, st.images, st.fresh, st.drifting, st.stale, st.abandoned, st.newest
			FROM kids k JOIN subtree_stats st ON st.id = k.digest WHERE k.descendants > 0
			UNION ALL
			SELECT '` + repoPrefix + `' || ? || '|' || i.repo, 'repo', i.repo, count(*),
				count(*) FILTER (WHERE s.verdict = 'fresh'), count(*) FILTER (WHERE s.verdict = 'drifting'),
				count(*) FILTER (WHERE s.verdict = 'stale'), count(*) FILTER (WHERE s.verdict = 'abandoned'),
				max(i.created) FILTER (WHERE year(i.created) >= 1980)
			FROM kids k JOIN images i ON i.digest = k.digest LEFT JOIN staleness s ON s.digest = k.digest
			WHERE k.descendants = 0
			GROUP BY i.repo
		)
		SELECT *, count(*) OVER () FROM rows WHERE ` + staleFilter(onlyStale) + `
		ORDER BY images DESC, id LIMIT ? OFFSET ?`
	if parent == Unrooted {
		// Lone images only; roots with descendants are listed at the top.
		q = strings.Replace(q, "FROM kids k JOIN subtree_stats st ON st.id = k.digest WHERE k.descendants > 0",
			"FROM kids k JOIN subtree_stats st ON st.id = k.digest WHERE false", 1)
	}
	args = append(args, parent)
	return s.fillRows(ctx, out, parent, 1, q, append(args, limit, offset)...)
}

// fillRows runs an outline query returning (id, t, repo, images, fresh,
// drifting, stale, abandoned, newest, total) and decorates the rows.
func (s *Service) fillRows(ctx context.Context, out *Outline, parent string, depth int, q string, args ...any) error {
	rows, err := s.db().QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("outline: %w", err)
	}
	var nodeIDs []string
	for rows.Next() {
		var r OutlineRow
		var t string
		var repo sql.NullString
		var newest sql.NullTime
		if err := rows.Scan(&r.ID, &t, &repo, &r.Images, &r.Verdicts.Fresh, &r.Verdicts.Drifting, &r.Verdicts.Stale, &r.Verdicts.Abandoned, &newest, &out.Total); err != nil {
			_ = rows.Close()
			return err
		}
		r.Parent, r.Depth, r.Repo, r.HasChildren = parent, depth, repo.String, true
		if newest.Valid {
			n := newest.Time.UTC()
			r.Newest = &n
		}
		switch t {
		case "unrooted":
			r.Kind, r.Name = Unrooted, "No known base"
		case "repo":
			r.Kind, r.Name = "repo", repo.String
		default:
			nodeIDs = append(nodeIDs, r.ID)
		}
		out.Rows = append(out.Rows, r)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if err := s.decorateNodes(ctx, out.Rows, nodeIDs); err != nil {
		return err
	}
	return s.decorateRepos(ctx, out.Rows, parent)
}

// decorateNodes names base rows and fills their kind, team and drift.
func (s *Service) decorateNodes(ctx context.Context, rows []OutlineRow, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	nodes, err := s.nodes(ctx, ids)
	if err != nil {
		return err
	}
	extra := map[string]struct {
		team  string
		drift int
	}{}
	q, err := s.db().QueryContext(ctx, `SELECT digest, coalesce(labels['team']::VARCHAR, ''), stale_base_drift FROM `+s.view()+` WHERE list_contains(?, digest)`, ids)
	if err != nil {
		return err
	}
	for q.Next() {
		var id, team string
		var drift int
		if err := q.Scan(&id, &team, &drift); err != nil {
			_ = q.Close()
			return err
		}
		extra[id] = struct {
			team  string
			drift int
		}{team, drift}
	}
	_ = q.Close()
	for i := range rows {
		n, ok := nodes[rows[i].ID]
		if !ok || rows[i].Kind != "" {
			continue
		}
		rows[i].Name, rows[i].Repo, rows[i].Digest = n.Label, n.Repo, strings.TrimPrefix(n.ID, model.PhantomPrefix)
		switch n.Kind {
		case graph.KindExternal:
			rows[i].Kind = "external"
		case graph.KindPhantom:
			rows[i].Kind = "phantom"
			if n.Note == "" {
				rows[i].Name = "Shared layers, image not in the registry"
			}
		default:
			rows[i].Kind = "base"
		}
		rows[i].Team, rows[i].Drift = extra[rows[i].ID].team, extra[rows[i].ID].drift
	}
	return q.Err()
}

// decorateRepos fills the team and the worst drift of repo group rows.
func (s *Service) decorateRepos(ctx context.Context, rows []OutlineRow, parent string) error {
	var repos []string
	for _, r := range rows {
		if r.Kind == "repo" {
			repos = append(repos, r.Repo)
		}
	}
	if len(repos) == 0 {
		return nil
	}
	cond, args := "l.parent = ?", []any{parent}
	if parent == Unrooted {
		cond, args = "l.parent IS NULL", nil
	}
	q, err := s.db().QueryContext(ctx, `
		SELECT v.repo, coalesce(mode(v.labels['team']::VARCHAR), ''), max(v.stale_base_drift)
		FROM lineage l JOIN `+s.view()+` v ON v.digest = l.digest
		WHERE `+cond+` AND l.descendants = 0 AND list_contains(?, v.repo)
		GROUP BY v.repo`, append(args, repos)...)
	if err != nil {
		return fmt.Errorf("repo rows: %w", err)
	}
	defer func() { _ = q.Close() }()
	info := map[string][2]any{}
	for q.Next() {
		var repo, team string
		var drift int
		if err := q.Scan(&repo, &team, &drift); err != nil {
			return err
		}
		info[repo] = [2]any{team, drift}
	}
	for i := range rows {
		if v, ok := info[rows[i].Repo]; ok && rows[i].Kind == "repo" {
			rows[i].Team, rows[i].Drift = v[0].(string), v[1].(int)
		}
	}
	return q.Err()
}

// outlineImages lists a repo group's images, newest first.
func (s *Service) outlineImages(ctx context.Context, out *Outline, parent, repo string, onlyStale bool, offset, limit int) error {
	cond, args := "l.parent = ?", []any{parent}
	if parent == Unrooted {
		cond, args = "l.parent IS NULL", nil
	}
	verdicts := "true"
	if onlyStale {
		verdicts = "v.stale IN ('stale', 'abandoned')"
	}
	q := `SELECT v.digest, v.repo, v.tag_refs, coalesce(v.stale, ''), coalesce(v.labels['team']::VARCHAR, ''), v.stale_base_drift,
			v.created, count(*) OVER ()
		FROM lineage l JOIN ` + s.view() + ` v ON v.digest = l.digest
		WHERE ` + cond + ` AND l.descendants = 0 AND v.repo = ? AND ` + verdicts + `
		ORDER BY v.created DESC NULLS LAST, v.digest LIMIT ? OFFSET ?`
	rows, err := s.db().QueryContext(ctx, q, append(args, repo, limit, offset)...)
	if err != nil {
		return fmt.Errorf("outline images: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		r := OutlineRow{Kind: "image", Parent: out.Parent, Depth: 2, Images: 1}
		var tags any
		var created sql.NullTime
		if err := rows.Scan(&r.Digest, &r.Repo, &tags, &r.Verdict, &r.Team, &r.Drift, &created, &out.Total); err != nil {
			return err
		}
		r.ID, r.Tags = r.Digest, toStrings(tags)
		r.Name = graph.Label(r.Repo, r.Digest, r.Tags)
		if created.Valid && created.Time.Year() >= 1980 {
			c := created.Time.UTC()
			r.Newest = &c
		}
		switch r.Verdict {
		case "fresh":
			r.Verdicts.Fresh = 1
		case "drifting":
			r.Verdicts.Drifting = 1
		case "stale":
			r.Verdicts.Stale = 1
		case "abandoned":
			r.Verdicts.Abandoned = 1
		}
		out.Rows = append(out.Rows, r)
	}
	return rows.Err()
}

// Map returns root bases and up to depth levels below them, with the
// largest children of each node kept and leaf images grouped by repo, so
// the result stays drawable at any registry size. It runs as a handful of
// set-based queries rather than one per node.
func (s *Service) Map(ctx context.Context, depth int) ([]OutlineRow, error) {
	if depth < 1 || depth > 6 {
		depth = 3
	}
	const perParent, maxRoots = 16, 40
	rows, err := s.db().QueryContext(ctx, `
		WITH n AS (
			SELECT l.digest AS id, l.parent, l.generation AS g, st.images, st.fresh, st.drifting, st.stale, st.abandoned, st.newest,
				row_number() OVER (PARTITION BY l.parent ORDER BY st.images DESC, l.digest) AS rn
			FROM lineage l JOIN subtree_stats st ON st.id = l.digest
			WHERE l.descendants > 0 AND l.generation < ?
		)
		SELECT id, coalesce(parent, ''), g, images, fresh, drifting, stale, abandoned, newest FROM n
		WHERE rn <= CASE WHEN parent IS NULL THEN ? ELSE ? END
		ORDER BY g, images DESC, id`, depth, maxRoots, perParent)
	if err != nil {
		return nil, fmt.Errorf("map nodes: %w", err)
	}
	kept := map[string]bool{}
	var out []OutlineRow
	var ids []string
	for rows.Next() {
		var r OutlineRow
		var newest sql.NullTime
		if err := rows.Scan(&r.ID, &r.Parent, &r.Depth, &r.Images, &r.Verdicts.Fresh, &r.Verdicts.Drifting, &r.Verdicts.Stale, &r.Verdicts.Abandoned, &newest); err != nil {
			_ = rows.Close()
			return nil, err
		}
		// Keep a node only when its parent made the cut.
		if r.Parent != "" && !kept[r.Parent] {
			continue
		}
		if newest.Valid {
			t := newest.Time.UTC()
			r.Newest = &t
		}
		r.HasChildren = true
		kept[r.ID] = true
		ids = append(ids, r.ID)
		out = append(out, r)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.decorateNodes(ctx, out, ids); err != nil {
		return nil, err
	}
	depthOf := map[string]int{}
	for _, r := range out {
		depthOf[r.ID] = r.Depth
	}
	// Leaf images grouped by repo under every kept node above the last level.
	var parents []string
	for _, r := range out {
		if r.Depth < depth {
			parents = append(parents, r.ID)
		}
	}
	if len(parents) > 0 {
		repos, err := s.db().QueryContext(ctx, `
			WITH g AS (
				SELECT l.parent, v.repo, count(*) AS images,
					count(*) FILTER (WHERE v.stale = 'fresh') AS fresh, count(*) FILTER (WHERE v.stale = 'drifting') AS drifting,
					count(*) FILTER (WHERE v.stale = 'stale') AS stale, count(*) FILTER (WHERE v.stale = 'abandoned') AS abandoned,
					max(v.created) FILTER (WHERE year(v.created) >= 1980) AS newest
				FROM lineage l JOIN `+s.view()+` v ON v.digest = l.digest
				WHERE l.descendants = 0 AND list_contains(?, l.parent)
				GROUP BY l.parent, v.repo
			)
			SELECT parent, repo, images, fresh, drifting, stale, abandoned, newest FROM (
				SELECT *, row_number() OVER (PARTITION BY parent ORDER BY images DESC, repo) AS rn FROM g
			) WHERE rn <= ? ORDER BY parent, images DESC`, parents, perParent)
		if err != nil {
			return nil, fmt.Errorf("map repos: %w", err)
		}
		for repos.Next() {
			r := OutlineRow{Kind: "repo", HasChildren: true}
			var newest sql.NullTime
			if err := repos.Scan(&r.Parent, &r.Repo, &r.Images, &r.Verdicts.Fresh, &r.Verdicts.Drifting, &r.Verdicts.Stale, &r.Verdicts.Abandoned, &newest); err != nil {
				_ = repos.Close()
				return nil, err
			}
			r.ID, r.Name, r.Depth = repoPrefix+r.Parent+"|"+r.Repo, r.Repo, depthOf[r.Parent]+1
			if newest.Valid {
				t := newest.Time.UTC()
				r.Newest = &t
			}
			out = append(out, r)
		}
		_ = repos.Close()
		if err := repos.Err(); err != nil {
			return nil, err
		}
	}
	// The unrooted pseudo-root and its repos close the map.
	un, err := s.Outline(ctx, "", false, 0, maxRoots+1)
	if err != nil {
		return nil, err
	}
	for _, r := range un.Rows {
		if r.Kind != Unrooted {
			continue
		}
		out = append(out, r)
		kids, err := s.Outline(ctx, Unrooted, false, 0, perParent)
		if err != nil {
			return nil, err
		}
		for i := range kids.Rows {
			kids.Rows[i].Depth = 1
		}
		out = append(out, kids.Rows...)
	}
	return out, nil
}

// RootStat is a root base and the verdicts of everything built on it.
type RootStat struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Kind     string        `json:"kind"`
	Images   int64         `json:"images"`
	Verdicts VerdictCounts `json:"verdicts"`
}

// Overview is the headline view of the index.
type Overview struct {
	Host          string        `json:"host,omitempty"`
	Images        int64         `json:"images"`
	Repos         int64         `json:"repos"`
	Tags          int64         `json:"tags"`
	Stacks        int64         `json:"stacks"`
	ApparentBytes int64         `json:"apparent_bytes"`
	DedupBytes    int64         `json:"dedup_bytes"`
	Verdicts      VerdictCounts `json:"verdicts"`
	Roots         []RootStat    `json:"roots"`
	Changes       []Change      `json:"changes"`
	IndexedAt     *time.Time    `json:"indexed_at,omitempty"`
}

// Overview gathers the headline numbers, the roots holding the most stale
// images and the last week's changes.
func (s *Service) Overview(ctx context.Context) (*Overview, error) {
	// Refresh already wrote today's totals to the snapshot; computing
	// deduplicated bytes again would scan every layer.
	ms, err := s.latestTotals(ctx)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		// No snapshot yet: the index has never been refreshed. Count from
		// the stored tables, since the full metrics would join the
		// unmaterialized view and take minutes on a large index; verdicts
		// stay zero until staleness is computed.
		m := GroupMetrics{Group: "all"}
		err := s.db().QueryRowContext(ctx, `SELECT
			(SELECT count(*) FROM images), (SELECT count(DISTINCT repo) FROM images),
			(SELECT count(*) FROM tags WHERE digest IN (SELECT digest FROM images) OR digest IN (SELECT index_digest FROM index_manifests)),
			(SELECT count(DISTINCT top_chain_id) FROM images),
			(SELECT coalesce(sum(size), 0) FROM images), (SELECT coalesce(sum(size), 0) FROM layers)`).Scan(
			&m.Manifests, &m.Repos, &m.Tags, &m.Stacks, &m.ApparentBytes, &m.DedupBytes)
		if err != nil {
			return nil, err
		}
		ms = []GroupMetrics{m}
	}
	o := &Overview{Roots: []RootStat{}, Changes: []Change{}}
	if len(ms) > 0 {
		m := ms[0]
		o.Images, o.Repos, o.Tags, o.Stacks = m.Manifests, m.Repos, m.Tags, m.Stacks
		o.ApparentBytes, o.DedupBytes = m.ApparentBytes, m.DedupBytes
		o.Verdicts = VerdictCounts{m.Fresh, m.Drifting, m.Stale, m.Abandoned}
	}
	hosts, err := s.strings(ctx, `SELECT DISTINCT split_part(repo, '/', 1) FROM images LIMIT 2`)
	if err != nil {
		return nil, err
	}
	if len(hosts) == 1 && strings.ContainsAny(hosts[0], ".:") {
		o.Host = hosts[0]
	}
	var indexed sql.NullTime
	if err := s.db().QueryRowContext(ctx, `SELECT max(indexed_at) FROM images`).Scan(&indexed); err != nil {
		return nil, err
	}
	if indexed.Valid {
		t := indexed.Time.UTC()
		o.IndexedAt = &t
	}
	roots, err := s.Outline(ctx, "", false, 0, 200)
	if err != nil {
		return nil, err
	}
	for _, r := range roots.Rows {
		o.Roots = append(o.Roots, RootStat{ID: r.ID, Name: r.Name, Kind: r.Kind, Images: r.Images, Verdicts: r.Verdicts})
	}
	sort.SliceStable(o.Roots, func(i, j int) bool {
		a, b := o.Roots[i].Verdicts, o.Roots[j].Verdicts
		return a.Stale+a.Abandoned > b.Stale+b.Abandoned
	})
	if len(o.Roots) > 8 {
		o.Roots = o.Roots[:8]
	}
	if d, err := s.Diff(ctx, 7*24*time.Hour, "", 90); err == nil {
		o.Changes = d.Changes
		if len(o.Changes) > 12 {
			o.Changes = o.Changes[:12]
		}
	}
	return o, nil
}

// SearchOptions are a filter plus facet selections.
type SearchOptions struct {
	Filter   string
	Team     []string
	Root     []string
	Verdict  []string
	Platform []string
	Age      []string
	Pushed   []string
	Offset   int
	Limit    int
}

// FacetValue is one value of a facet.
type FacetValue struct {
	Value    string `json:"value"`
	Count    int64  `json:"count"`
	Selected bool   `json:"selected"`
}

// Facet is a field the search can be narrowed by.
type Facet struct {
	Name   string       `json:"name"`
	Field  string       `json:"field"`
	Values []FacetValue `json:"values"`
}

// SearchHit is one image in search results.
type SearchHit struct {
	Digest   string   `json:"digest"`
	Repo     string   `json:"repo"`
	Name     string   `json:"name"`
	Tags     []string `json:"tags"`
	Team     string   `json:"team,omitempty"`
	Root     string   `json:"root,omitempty"`
	Platform string   `json:"platform"`
	Verdict  string   `json:"verdict"`
	AgeDays  *int     `json:"age_days,omitempty"`
	Size     int64    `json:"size"`
}

// SearchResult is a page of hits with facet counts.
type SearchResult struct {
	Total  int64       `json:"total"`
	Offset int         `json:"offset"`
	Filter string      `json:"filter"`
	Hits   []SearchHit `json:"hits"`
	Facets []Facet     `json:"facets"`
}

// dayBucket groups a column of days into the age buckets.
func dayBucket(col string) string {
	return `CASE WHEN ` + col + ` IS NULL THEN 'unknown' WHEN ` + col + ` < 30 THEN '< 30 days'
	WHEN ` + col + ` < 180 THEN '30–180 days' WHEN ` + col + ` < 365 THEN '180 days–1 year' ELSE '> 1 year' END`
}

// dayFilter is the query-language form of an age bucket over a days
// field, such as age or pushed_age.
func dayFilter(field, bucket string) (string, bool) {
	f, ok := map[string]string{
		"< 30 days": "F < 30d", "30–180 days": "(F >= 30d and F < 180d)",
		"180 days–1 year": "(F >= 180d and F < 1y)", "> 1 year": "F >= 1y", "unknown": "not F exists",
	}[bucket]
	return strings.ReplaceAll(f, "F", field), ok
}

// dayFacets are the facets over age buckets, in bucket order.
var dayFacets = map[string]bool{"Age": true, "Pushed": true}

// facetDef describes one facet: its name, the query-language field, the
// SQL expression it groups by, and the selected values.
type facetDef struct {
	name, field, expr string
	selected          []string
}

// Search runs a filter with facet selections and returns a page of hits
// and every facet's counts. Each facet's counts apply every selection
// except its own.
func (s *Service) Search(ctx context.Context, o SearchOptions) (*SearchResult, error) {
	c, err := query.Compile(o.Filter)
	if err != nil {
		return nil, input(err)
	}
	if o.Limit <= 0 || o.Limit > 500 {
		o.Limit = 50
	}
	rootExpr, err := groupExpr("root")
	if err != nil {
		return nil, err
	}
	facets := []facetDef{
		{"Verdict", "stale", "stale", o.Verdict},
		{"Team", "label.team", "labels['team']::VARCHAR", o.Team},
		{"Root base", "root", rootExpr, o.Root},
		{"Platform", "platform", "platform", o.Platform},
		{"Age", "age", dayBucket("age_days"), o.Age},
		{"Pushed", "pushed_age", dayBucket("pushed_age_days"), o.Pushed},
	}
	where := func(skip int) (string, []any) {
		parts, args := []string{c.Where}, append([]any{}, c.Args...)
		for i, f := range facets {
			if i == skip || len(f.selected) == 0 {
				continue
			}
			ph := make([]string, len(f.selected))
			for j, v := range f.selected {
				ph[j] = "?"
				args = append(args, v)
			}
			parts = append(parts, "("+f.expr+") IN ("+strings.Join(ph, ", ")+")")
		}
		return strings.Join(parts, " AND "), args
	}
	res := &SearchResult{Offset: max(0, o.Offset), Hits: []SearchHit{}, Facets: []Facet{}, Filter: foldFilter(o.Filter, facets)}
	w, args := where(-1)
	rows, err := s.db().QueryContext(ctx, `
		SELECT digest, repo, tag_refs, coalesce(labels['team']::VARCHAR, ''), coalesce(`+rootExpr+`, ''), platform,
			coalesce(stale, ''), age_days, coalesce(size, 0), count(*) OVER ()
		FROM `+s.view()+` WHERE `+w+`
		ORDER BY created DESC NULLS LAST, digest LIMIT ? OFFSET ?`, append(args, o.Limit, res.Offset)...)
	if err != nil {
		return nil, input(fmt.Errorf("run search: %w", err))
	}
	for rows.Next() {
		var h SearchHit
		var tags any
		var age sql.NullInt64
		if err := rows.Scan(&h.Digest, &h.Repo, &tags, &h.Team, &h.Root, &h.Platform, &h.Verdict, &age, &h.Size, &res.Total); err != nil {
			_ = rows.Close()
			return nil, err
		}
		h.Tags = toStrings(tags)
		h.Name = graph.Label(h.Repo, h.Digest, h.Tags)
		if age.Valid {
			a := int(age.Int64)
			h.AgeDays = &a
		}
		res.Hits = append(res.Hits, h)
	}
	_ = rows.Close()
	if res.Offset > 0 && len(res.Hits) == 0 {
		// Past the end: count without paging.
		if err := s.db().QueryRowContext(ctx, `SELECT count(*) FROM `+s.view()+` WHERE `+w, args...).Scan(&res.Total); err != nil {
			return nil, err
		}
	}
	for i, f := range facets {
		fw, fargs := where(i)
		vals, err := s.facetValues(ctx, f, fw, fargs)
		if err != nil {
			return nil, err
		}
		// Registries without push times leave every image unknown; the
		// facet would say nothing.
		if f.name == "Pushed" && len(f.selected) == 0 && onlyUnknown(vals) {
			continue
		}
		res.Facets = append(res.Facets, Facet{Name: f.name, Field: f.field, Values: vals})
	}
	return res, nil
}

// facetValues counts a facet's values under a condition, always keeping
// the selected ones.
func (s *Service) facetValues(ctx context.Context, f facetDef, where string, args []any) ([]FacetValue, error) {
	limit := 10
	order := "n DESC, v"
	if f.name == "Verdict" || dayFacets[f.name] {
		limit, order = 10, "v"
	}
	rows, err := s.db().QueryContext(ctx, `SELECT coalesce(CAST(`+f.expr+` AS VARCHAR), '') AS v, count(*) AS n
		FROM `+s.view()+` WHERE `+where+` GROUP BY 1 ORDER BY `+order+` LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		return nil, fmt.Errorf("facet %s: %w", f.name, err)
	}
	defer func() { _ = rows.Close() }()
	selected := map[string]bool{}
	for _, v := range f.selected {
		selected[v] = true
	}
	out := []FacetValue{}
	seen := map[string]bool{}
	for rows.Next() {
		var fv FacetValue
		if err := rows.Scan(&fv.Value, &fv.Count); err != nil {
			return nil, err
		}
		if fv.Value == "" {
			continue
		}
		fv.Selected = selected[fv.Value]
		seen[fv.Value] = true
		out = append(out, fv)
	}
	for _, v := range f.selected {
		if !seen[v] {
			out = append(out, FacetValue{Value: v, Selected: true})
		}
	}
	if f.name == "Verdict" {
		rank := map[string]int{"fresh": 0, "drifting": 1, "stale": 2, "abandoned": 3}
		sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Value] < rank[out[j].Value] })
	}
	if dayFacets[f.name] {
		rank := map[string]int{"< 30 days": 0, "30–180 days": 1, "180 days–1 year": 2, "> 1 year": 3, "unknown": 4}
		sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Value] < rank[out[j].Value] })
	}
	return out, rows.Err()
}

// onlyUnknown reports whether a facet has no value but unknown.
func onlyUnknown(vals []FacetValue) bool {
	for _, v := range vals {
		if v.Value != "unknown" {
			return false
		}
	}
	return true
}

// foldFilter writes the filter with every facet selection folded in, as
// strata query takes it.
func foldFilter(filter string, facets []facetDef) string {
	var parts []string
	if strings.TrimSpace(filter) != "" {
		parts = append(parts, strings.TrimSpace(filter))
	}
	quote := func(vs []string) string {
		q := make([]string, len(vs))
		for i, v := range vs {
			q[i] = query.Value{Quoted: true, Text: v}.String()
		}
		return "[" + strings.Join(q, ", ") + "]"
	}
	for _, f := range facets {
		if len(f.selected) == 0 {
			continue
		}
		if f.field == "age" || f.field == "pushed_age" {
			var or []string
			for _, v := range f.selected {
				if a, ok := dayFilter(f.field, v); ok {
					or = append(or, a)
				}
			}
			if len(or) > 0 {
				parts = append(parts, "("+strings.Join(or, " or ")+")")
			}
			continue
		}
		parts = append(parts, f.field+" in "+quote(f.selected))
	}
	if len(parts) > 1 && strings.TrimSpace(filter) != "" && strings.Contains(strings.ToLower(filter), " or ") {
		parts[0] = "(" + parts[0] + ")"
	}
	return strings.Join(parts, " and ")
}

// latestTotals reads the whole-index metrics from the newest snapshot, or
// returns nothing when there is no snapshot yet.
func (s *Service) latestTotals(ctx context.Context) ([]GroupMetrics, error) {
	rows, err := s.db().QueryContext(ctx, `SELECT metric, value FROM snapshots
		WHERE group_by = 'all' AND day = (SELECT max(day) FROM snapshots WHERE group_by = 'all')`)
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	defer func() { _ = rows.Close() }()
	v := map[string]float64{}
	for rows.Next() {
		var k string
		var x float64
		if err := rows.Scan(&k, &x); err != nil {
			return nil, err
		}
		v[k] = x
	}
	if len(v) == 0 {
		return nil, rows.Err()
	}
	i := func(k string) int64 { return int64(v[k]) }
	return []GroupMetrics{{
		Group: "all", Repos: i("repos"), Tags: i("tags"), Manifests: i("manifests"), Stacks: i("unique_stacks"),
		UntaggedPct: v["untagged_pct"], ApparentBytes: i("apparent_bytes"), DedupBytes: i("dedup_bytes"),
		ExclusiveBytes: i("exclusive_bytes"), SharedBytes: i("shared_cost_bytes"),
		Fresh: i("fresh"), Drifting: i("drifting"), Stale: i("stale"), Abandoned: i("abandoned"),
	}}, rows.Err()
}
