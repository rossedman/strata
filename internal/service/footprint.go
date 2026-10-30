package service

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// GroupMetrics are the footprint numbers for one group of images.
type GroupMetrics struct {
	Group          string  `json:"group"`
	Repos          int64   `json:"repos"`
	Tags           int64   `json:"tags"`
	Manifests      int64   `json:"manifests"`
	Stacks         int64   `json:"unique_stacks"`
	UntaggedPct    float64 `json:"untagged_pct"`
	ApparentBytes  int64   `json:"apparent_bytes"`
	DedupBytes     int64   `json:"dedup_bytes"`
	ExclusiveBytes int64   `json:"exclusive_bytes"`
	SharedBytes    int64   `json:"shared_cost_bytes"`
	Fresh          int64   `json:"fresh"`
	Drifting       int64   `json:"drifting"`
	Stale          int64   `json:"stale"`
	Abandoned      int64   `json:"abandoned"`
}

// Summary is the shape of the registry in one screen.
type Summary struct {
	GroupBy    string             `json:"group_by,omitempty"`
	Total      GroupMetrics       `json:"total"`
	Groups     []GroupMetrics     `json:"groups,omitempty"`
	Platforms  map[string]int64   `json:"platforms"`
	Compliance map[string]float64 `json:"required_label_coverage,omitempty"`
}

// Summary computes footprint metrics for the whole index and, with
// groupBy, per repo, root or label.
func (s *Service) Summary(ctx context.Context, groupBy string) (*Summary, error) {
	out := &Summary{GroupBy: groupBy, Platforms: map[string]int64{}}
	totals, err := s.groupMetrics(ctx, "")
	if err != nil {
		return nil, err
	}
	if len(totals) > 0 {
		out.Total = totals[0]
	}
	out.Total.Group = "all"
	if groupBy != "" {
		if out.Groups, err = s.groupMetrics(ctx, groupBy); err != nil {
			return nil, err
		}
	}
	rows, err := s.db().QueryContext(ctx, `SELECT platform, count(*) FROM `+s.view()+` GROUP BY 1 ORDER BY 2 DESC`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p sql.NullString
		var n int64
		if err := rows.Scan(&p, &n); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out.Platforms[p.String] = n
	}
	_ = rows.Close()
	if len(s.Required) > 0 {
		out.Compliance = map[string]float64{}
		for _, k := range s.Required {
			var pct sql.NullFloat64
			err := s.db().QueryRowContext(ctx, `SELECT round(100.0 * count(*) FILTER (WHERE labels[?] IS NOT NULL) / nullif(count(*), 0), 1) FROM `+s.view()+``, k).Scan(&pct)
			if err != nil {
				return nil, fmt.Errorf("required label %s: %w", k, err)
			}
			out.Compliance[k] = pct.Float64
		}
	}
	return out, nil
}

// groupMetrics computes GroupMetrics per value of a grouping field.
func (s *Service) groupMetrics(ctx context.Context, groupBy string) ([]GroupMetrics, error) {
	g, err := groupExpr(groupBy)
	if err != nil {
		return nil, input(err)
	}
	q := `
		WITH g AS (SELECT *, coalesce(CAST(` + g + ` AS VARCHAR), '(none)') AS grp FROM ` + s.view() + `),
		bl AS (SELECT DISTINCT g.grp, il.blob_digest, il.size FROM image_layers il JOIN g ON g.digest = il.digest),
		bc AS (SELECT blob_digest, count(DISTINCT grp) AS n FROM bl GROUP BY 1),
		bytes AS (
			SELECT bl.grp, sum(bl.size) AS dedup,
				coalesce(sum(bl.size) FILTER (WHERE bc.n = 1), 0) AS excl,
				sum(bl.size / bc.n) AS shared
			FROM bl JOIN bc USING (blob_digest) GROUP BY 1),
		tg AS (SELECT grp, count(DISTINCT t) AS tags FROM (SELECT grp, unnest(tag_refs) AS t FROM g) GROUP BY 1)
		SELECT g.grp, count(DISTINCT g.repo), coalesce(any_value(tg.tags), 0), count(*), count(DISTINCT g.top_chain_id),
			round(100.0 * count(*) FILTER (WHERE g.tags = 0) / count(*), 1),
			coalesce(sum(g.size), 0), coalesce(any_value(bytes.dedup), 0), coalesce(any_value(bytes.excl), 0),
			coalesce(any_value(bytes.shared), 0)::BIGINT,
			count(*) FILTER (WHERE g.stale = 'fresh'), count(*) FILTER (WHERE g.stale = 'drifting'),
			count(*) FILTER (WHERE g.stale = 'stale'), count(*) FILTER (WHERE g.stale = 'abandoned')
		FROM g LEFT JOIN bytes ON bytes.grp = g.grp LEFT JOIN tg ON tg.grp = g.grp
		GROUP BY g.grp ORDER BY count(*) DESC, g.grp`
	rows, err := s.db().QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("summary: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []GroupMetrics
	for rows.Next() {
		var m GroupMetrics
		var pct sql.NullFloat64
		if err := rows.Scan(&m.Group, &m.Repos, &m.Tags, &m.Manifests, &m.Stacks, &pct, &m.ApparentBytes,
			&m.DedupBytes, &m.ExclusiveBytes, &m.SharedBytes, &m.Fresh, &m.Drifting, &m.Stale, &m.Abandoned); err != nil {
			return nil, err
		}
		m.UntaggedPct = pct.Float64
		out = append(out, m)
	}
	return out, rows.Err()
}

// LabelKey profiles one label or annotation key.
type LabelKey struct {
	Key         string   `json:"key"`
	Images      int64    `json:"images"`
	CoveragePct float64  `json:"coverage_pct"`
	Distinct    int64    `json:"distinct_values"`
	TopValues   []string `json:"top_values"`
	Required    bool     `json:"required,omitempty"`
}

// Labels profiles every label and annotation key.
func (s *Service) Labels(ctx context.Context) ([]LabelKey, error) {
	rows, err := s.db().QueryContext(ctx, `
		WITH kv AS (
			SELECT l.digest, j.key, CASE WHEN json_type(j.value) = 'VARCHAR' THEN j.value ->> '$' ELSE j.value::VARCHAR END AS value
			FROM labels l, json_each(l.labels::JSON) j),
		total AS (SELECT count(*) AS n FROM images),
		vc AS (SELECT key, value, count(*) AS n FROM kv GROUP BY 1, 2),
		top AS (SELECT key, list(value || ' (' || n || ')' ORDER BY n DESC, value)[1:3] AS top FROM vc GROUP BY 1)
		SELECT kv.key, count(DISTINCT kv.digest), round(100.0 * count(DISTINCT kv.digest) / any_value(total.n), 1),
			count(DISTINCT kv.value), any_value(top.top)
		FROM kv, total, top WHERE top.key = kv.key
		GROUP BY kv.key ORDER BY 2 DESC, 1`)
	if err != nil {
		return nil, fmt.Errorf("profile labels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	required := map[string]bool{}
	for _, k := range s.Required {
		required[k] = true
	}
	out := []LabelKey{}
	for rows.Next() {
		var k LabelKey
		var top any
		if err := rows.Scan(&k.Key, &k.Images, &k.CoveragePct, &k.Distinct, &top); err != nil {
			return nil, err
		}
		k.TopValues = unsorted(top)
		k.Required = required[k.Key]
		delete(required, k.Key)
		out = append(out, k)
	}
	missing := make([]string, 0, len(required))
	for k := range required {
		missing = append(missing, k)
	}
	sort.Strings(missing)
	for _, k := range missing {
		out = append(out, LabelKey{Key: k, Required: true, TopValues: []string{}})
	}
	return out, rows.Err()
}

// LabelDetail describes one key: its values, the repos lacking it and
// when it was first and last seen.
type LabelDetail struct {
	Key       string           `json:"key"`
	Values    map[string]int64 `json:"values"`
	Lacking   map[string]int64 `json:"repos_lacking"`
	FirstSeen *time.Time       `json:"first_seen,omitempty"`
	LastSeen  *time.Time       `json:"last_seen,omitempty"`
	Images    int64            `json:"images"`
	Kind      string           `json:"kind"`
}

// Label profiles a single key.
func (s *Service) Label(ctx context.Context, key string) (*LabelDetail, error) {
	d := &LabelDetail{Key: key, Values: map[string]int64{}, Lacking: map[string]int64{}}
	rows, err := s.db().QueryContext(ctx, `SELECT labels[?]::VARCHAR, count(*) FROM `+s.view()+` WHERE labels[?] IS NOT NULL GROUP BY 1`, key, key)
	if err != nil {
		return nil, fmt.Errorf("label values: %w", err)
	}
	for rows.Next() {
		var v string
		var n int64
		if err := rows.Scan(&v, &n); err != nil {
			_ = rows.Close()
			return nil, err
		}
		d.Values[v] = n
		d.Images += n
	}
	_ = rows.Close()
	rows, err = s.db().QueryContext(ctx, `SELECT repo, count(*) FROM `+s.view()+` WHERE labels[?] IS NULL GROUP BY 1`, key)
	if err != nil {
		return nil, fmt.Errorf("label gaps: %w", err)
	}
	for rows.Next() {
		var r string
		var n int64
		if err := rows.Scan(&r, &n); err != nil {
			_ = rows.Close()
			return nil, err
		}
		d.Lacking[r] = n
	}
	_ = rows.Close()
	var first, last sql.NullTime
	if err := s.db().QueryRowContext(ctx, `SELECT min(created), max(created) FROM `+s.view()+` WHERE labels[?] IS NOT NULL AND year(created) >= 1980`, key).Scan(&first, &last); err != nil {
		return nil, err
	}
	if first.Valid {
		f, l := first.Time.UTC(), last.Time.UTC()
		d.FirstSeen, d.LastSeen = &f, &l
	}
	d.Kind = "enum"
	if d.Images > 0 && float64(len(d.Values)) > 0.5*float64(d.Images) && len(d.Values) > 10 {
		d.Kind = "free text"
	}
	return d, nil
}

// SimilarKeys groups label keys that probably mean the same thing: equal
// after case folding and dropping separators, or linked by a synonym.
func (s *Service) SimilarKeys(ctx context.Context) ([][]string, error) {
	keys, err := s.strings(ctx, `SELECT DISTINCT unnest(variant_keys(labels)) FROM labels ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	return similarKeys(keys, s.Synonyms), nil
}

// similarKeys groups keys by their normalized form.
func similarKeys(keys []string, synonyms map[string]string) [][]string {
	groups := map[string][]string{}
	for _, k := range keys {
		n := NormalizeKey(k)
		if to, ok := synonyms[n]; ok {
			n = NormalizeKey(to)
		}
		groups[n] = append(groups[n], k)
	}
	out := [][]string{}
	for _, g := range groups {
		if len(g) > 1 {
			sort.Strings(g)
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// NormalizeKey folds case and drops separators, so team, Team, TEAM and
// t-e_a.m compare equal; a trailing name or id suffix is dropped too
// (team_name, team-id).
func NormalizeKey(k string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(k) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	n := b.String()
	for _, suf := range []string{"name", "id"} {
		if strings.HasSuffix(n, suf) && len(n) > len(suf)+2 {
			n = strings.TrimSuffix(n, suf)
		}
	}
	return n
}
