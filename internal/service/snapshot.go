package service

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// snapshotMetrics names the GroupMetrics fields written to snapshots.
var snapshotMetrics = []string{
	"repos", "tags", "manifests", "unique_stacks", "untagged_pct", "apparent_bytes", "dedup_bytes",
	"exclusive_bytes", "shared_cost_bytes", "fresh", "drifting", "stale", "abandoned",
}

// metricValues returns a GroupMetrics as snapshot metric values.
func metricValues(m GroupMetrics) map[string]float64 {
	return map[string]float64{
		"repos": float64(m.Repos), "tags": float64(m.Tags), "manifests": float64(m.Manifests),
		"unique_stacks": float64(m.Stacks), "untagged_pct": m.UntaggedPct,
		"apparent_bytes": float64(m.ApparentBytes), "dedup_bytes": float64(m.DedupBytes),
		"exclusive_bytes": float64(m.ExclusiveBytes), "shared_cost_bytes": float64(m.SharedBytes),
		"fresh": float64(m.Fresh), "drifting": float64(m.Drifting), "stale": float64(m.Stale), "abandoned": float64(m.Abandoned),
	}
}

// SnapshotGroups are the groupings recorded every day, besides the total.
var SnapshotGroups = []string{"repo", "root"}

// Snapshot writes today's summary metrics, one row per group and metric,
// replacing any earlier snapshot from the same day. Snapshots start on the
// first run, like tag history, so they cannot be backfilled.
func (s *Service) Snapshot(ctx context.Context) error {
	day := s.Now().UTC().Format(time.DateOnly)
	groups := append([]string{""}, SnapshotGroups...)
	for _, k := range s.Required {
		groups = append(groups, "label."+k)
	}
	tx, err := s.db().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM snapshots WHERE day = ?`, day); err != nil {
		return fmt.Errorf("clear snapshot: %w", err)
	}
	for _, g := range groups {
		ms, err := s.groupMetrics(ctx, g)
		if err != nil {
			return err
		}
		name := g
		if name == "" {
			name = "all"
		}
		for _, m := range ms {
			vals := metricValues(m)
			for _, k := range snapshotMetrics {
				if _, err := tx.ExecContext(ctx, `INSERT INTO snapshots VALUES (?, ?, ?, ?, ?)`, day, name, m.Group, k, vals[k]); err != nil {
					return fmt.Errorf("write snapshot: %w", err)
				}
			}
		}
	}
	keys, err := s.Labels(ctx)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if _, err := tx.ExecContext(ctx, `INSERT INTO snapshots VALUES (?, 'label_coverage', ?, 'coverage_pct', ?)`, day, k.Key, k.CoveragePct); err != nil {
			return fmt.Errorf("write label coverage: %w", err)
		}
	}
	return tx.Commit()
}

// Change is one line of a diff report.
type Change struct {
	Kind   string  `json:"kind"`
	Group  string  `json:"group"`
	Metric string  `json:"metric,omitempty"`
	From   float64 `json:"from"`
	To     float64 `json:"to"`
}

// Diff is what changed between two snapshots.
type Diff struct {
	From      string   `json:"from"`
	To        string   `json:"to"`
	Changes   []Change `json:"changes"`
	DarkRepos []string `json:"dark_repos"`
}

// Diff compares the latest snapshot with the one closest to since ago,
// and lists repos that went dark: no pushes in darkDays while still
// holding tagged images.
func (s *Service) Diff(ctx context.Context, since time.Duration, groupBy string, darkDays int) (*Diff, error) {
	d := &Diff{Changes: []Change{}, DarkRepos: []string{}}
	var to, from sql.NullString
	if err := s.db().QueryRowContext(ctx, `SELECT strftime(max(day), '%Y-%m-%d') FROM snapshots`).Scan(&to); err != nil {
		return nil, err
	}
	if !to.Valid {
		return nil, fmt.Errorf("no snapshots yet; run strata index first")
	}
	cutoff := s.Now().Add(-since).UTC().Format(time.DateOnly)
	if err := s.db().QueryRowContext(ctx, `SELECT strftime(coalesce(max(day) FILTER (WHERE day <= ?), min(day)), '%Y-%m-%d') FROM snapshots`, cutoff).Scan(&from); err != nil {
		return nil, err
	}
	d.From, d.To = from.String, to.String
	scope := "all"
	if groupBy != "" {
		scope = groupBy
	}
	type key struct{ group, metric string }
	read := func(day, groupBy string) (map[key]float64, error) {
		rows, err := s.db().QueryContext(ctx, `SELECT group_value, metric, value FROM snapshots WHERE day = ? AND group_by = ?`, day, groupBy)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		out := map[key]float64{}
		for rows.Next() {
			var k key
			var v float64
			if err := rows.Scan(&k.group, &k.metric, &v); err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, rows.Err()
	}
	for _, gb := range []string{scope, "repo", "root", "label_coverage"} {
		a, err := read(d.From, gb)
		if err != nil {
			return nil, err
		}
		b, err := read(d.To, gb)
		if err != nil {
			return nil, err
		}
		switch gb {
		case "repo", "root":
			kind := map[string]string{"repo": "repo", "root": "root base"}[gb]
			for k := range b {
				if _, ok := a[k]; !ok && k.metric == "manifests" {
					d.Changes = append(d.Changes, Change{Kind: "new " + kind, Group: k.group, To: b[k]})
				}
			}
			for k := range a {
				if _, ok := b[k]; !ok && k.metric == "manifests" && gb == "repo" {
					d.Changes = append(d.Changes, Change{Kind: "removed repo", Group: k.group, From: a[k]})
				}
			}
		default:
			for k, v := range b {
				if a[k] != v {
					kind := "metric"
					if gb == "label_coverage" {
						kind = "label coverage"
					}
					d.Changes = append(d.Changes, Change{Kind: kind, Group: k.group, Metric: k.metric, From: a[k], To: v})
				}
			}
		}
	}
	sort.SliceStable(d.Changes, func(i, j int) bool {
		a, b := d.Changes[i], d.Changes[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		return a.Metric < b.Metric
	})
	dark, err := s.strings(ctx, `
		SELECT t.repo FROM tags t
		LEFT JOIN (SELECT repo, max(seen_at) AS last FROM tag_history WHERE digest IS NOT NULL GROUP BY repo) h USING (repo)
		GROUP BY t.repo HAVING coalesce(any_value(h.last), min(t.updated_at)) < ?::TIMESTAMPTZ ORDER BY 1`,
		s.Now().AddDate(0, 0, -darkDays).UTC())
	if err != nil {
		return nil, fmt.Errorf("dark repos: %w", err)
	}
	d.DarkRepos = append(d.DarkRepos, dark...)
	return d, nil
}

// Point is one day's value of a metric.
type Point struct {
	Day   string  `json:"day"`
	Group string  `json:"group"`
	Value float64 `json:"value"`
}

// Trend returns a snapshot metric over time, per group.
func (s *Service) Trend(ctx context.Context, metric, groupBy string) ([]Point, error) {
	if !slices.Contains(snapshotMetrics, metric) {
		return nil, input(fmt.Errorf("unknown metric %q (one of %s)", metric, strings.Join(snapshotMetrics, ", ")))
	}
	scope := "all"
	if groupBy != "" {
		scope = groupBy
	}
	rows, err := s.db().QueryContext(ctx, `SELECT strftime(day, '%Y-%m-%d'), group_value, value FROM snapshots
		WHERE metric = ? AND group_by = ? ORDER BY day, group_value`, metric, scope)
	if err != nil {
		return nil, fmt.Errorf("read trend: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Point{}
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.Day, &p.Group, &p.Value); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if len(out) == 0 && groupBy != "" {
		return nil, fmt.Errorf("no snapshots grouped by %s; snapshots record all, %s and any required labels",
			groupBy, strings.Join(SnapshotGroups, ", "))
	}
	return out, rows.Err()
}
