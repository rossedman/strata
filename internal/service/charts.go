package service

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rossedman/strata/internal/model"
)

// The chart queries behind the web UI's Overview trend lines, Insights
// and Bases views. Each aggregates in SQL, so its cost and its response
// stay small however many images are indexed.

// cutoff is the first day a chart covering the last days days includes.
func (s *Service) cutoff(days int) string {
	return s.Now().UTC().AddDate(0, 0, -days).Format(time.DateOnly)
}

// clampDays keeps a window between 1 day and 2 years, defaulting to def.
func clampDays(days, def int) int {
	if days <= 0 {
		return def
	}
	return min(days, 731)
}

// KPIDay is one day of the whole-index numbers the Overview shows.
type KPIDay struct {
	Day        string `json:"day"`
	Images     int64  `json:"images"`
	Repos      int64  `json:"repos"`
	Stacks     int64  `json:"unique_stacks"`
	DedupBytes int64  `json:"dedup_bytes"`
	Stale      int64  `json:"stale"`
	Abandoned  int64  `json:"abandoned"`
}

// KPIHistory returns the whole-index snapshot numbers for the last days
// days, oldest first.
func (s *Service) KPIHistory(ctx context.Context, days int) ([]KPIDay, error) {
	rows, err := s.db().QueryContext(ctx, `
		SELECT strftime(day, '%Y-%m-%d'),
			coalesce(max(value) FILTER (WHERE metric = 'manifests'), 0)::BIGINT,
			coalesce(max(value) FILTER (WHERE metric = 'repos'), 0)::BIGINT,
			coalesce(max(value) FILTER (WHERE metric = 'unique_stacks'), 0)::BIGINT,
			coalesce(max(value) FILTER (WHERE metric = 'dedup_bytes'), 0)::BIGINT,
			coalesce(max(value) FILTER (WHERE metric = 'stale'), 0)::BIGINT,
			coalesce(max(value) FILTER (WHERE metric = 'abandoned'), 0)::BIGINT
		FROM snapshots WHERE group_by = 'all' AND day >= ?::DATE
		GROUP BY day ORDER BY day`, s.cutoff(clampDays(days, 30)))
	if err != nil {
		return nil, fmt.Errorf("read kpi history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []KPIDay{}
	for rows.Next() {
		var d KPIDay
		if err := rows.Scan(&d.Day, &d.Images, &d.Repos, &d.Stacks, &d.DedupBytes, &d.Stale, &d.Abandoned); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// StorageSeries is one group's stored bytes per day.
type StorageSeries struct {
	Group string `json:"group"`
	// Label names a root base that is not indexed: the base its builds'
	// provenance names, else "not in registry" and its short digest.
	Label string `json:"label,omitempty"`
	// Other is true for the series that folds every group outside the top.
	Other  bool    `json:"other,omitempty"`
	Values []int64 `json:"values"`
}

// Storage is stored bytes over time, split by root base or repo.
type Storage struct {
	GroupBy string          `json:"group_by"`
	Days    []string        `json:"days"`
	Series  []StorageSeries `json:"series"`
}

// Storage returns each group's share of the deduplicated bytes per day
// for the last days days: the top groups by their latest size, and the
// rest folded into one. A layer several groups use is split between them,
// so the series add up to the registry's deduplicated total.
func (s *Service) Storage(ctx context.Context, groupBy string, top, days int) (*Storage, error) {
	if groupBy == "" {
		groupBy = "root"
	}
	if groupBy != "root" && groupBy != "repo" {
		return nil, input(fmt.Errorf("cannot chart storage by %q: use root or repo", groupBy))
	}
	if top <= 0 || top > 12 {
		top = 6
	}
	rows, err := s.db().QueryContext(ctx, `
		WITH s AS (
			SELECT day, group_value, value FROM snapshots
			WHERE group_by = ? AND metric = 'shared_cost_bytes' AND day >= ?::DATE),
		latest AS (
			SELECT group_value FROM s WHERE day = (SELECT max(day) FROM s)
			ORDER BY value DESC, group_value LIMIT ?)
		SELECT strftime(day, '%Y-%m-%d'),
			CASE WHEN group_value IN (SELECT group_value FROM latest) THEN group_value END AS g,
			sum(value)::BIGINT
		FROM s GROUP BY ALL ORDER BY 1`, groupBy, s.cutoff(clampDays(days, 180)), top)
	if err != nil {
		return nil, fmt.Errorf("read storage history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := &Storage{GroupBy: groupBy, Days: []string{}, Series: []StorageSeries{}}
	values := map[string]map[int]int64{}
	latest := map[string]int64{}
	for rows.Next() {
		var day string
		var g sql.NullString
		var v int64
		if err := rows.Scan(&day, &g, &v); err != nil {
			return nil, err
		}
		if len(out.Days) == 0 || out.Days[len(out.Days)-1] != day {
			out.Days = append(out.Days, day)
		}
		key := g.String
		if !g.Valid {
			key = "\x00other"
		}
		if values[key] == nil {
			values[key] = map[int]int64{}
		}
		values[key][len(out.Days)-1] = v
		latest[key] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	// Largest first; the folded rest always last.
	sort.Slice(keys, func(i, j int) bool {
		oi, oj := strings.HasPrefix(keys[i], "\x00"), strings.HasPrefix(keys[j], "\x00")
		if oi != oj {
			return oj
		}
		if latest[keys[i]] != latest[keys[j]] {
			return latest[keys[i]] > latest[keys[j]]
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		ser := StorageSeries{Group: k, Values: make([]int64, len(out.Days))}
		if strings.HasPrefix(k, "\x00") {
			ser.Group, ser.Other = "other", true
		}
		for i, v := range values[k] {
			ser.Values[i] = v
		}
		out.Series = append(out.Series, ser)
	}
	var phantoms []string
	for _, ser := range out.Series {
		if model.IsPhantom(ser.Group) {
			phantoms = append(phantoms, ser.Group)
		}
	}
	if len(phantoms) > 0 {
		nodes, err := s.nodes(ctx, phantoms)
		if err != nil {
			return nil, err
		}
		for i, ser := range out.Series {
			if n, ok := nodes[ser.Group]; ok {
				out.Series[i].Label = n.Label
			}
		}
	}
	return out, nil
}

// DayCount is a count on one day.
type DayCount struct {
	Day   string `json:"day"`
	Count int64  `json:"count"`
}

// RepoCount is a count for one repo.
type RepoCount struct {
	Repo  string `json:"repo"`
	Count int64  `json:"count"`
}

// Activity is images pushed per day.
type Activity struct {
	// Source is pushed when the registry reports push times, else created:
	// the build time from the image config.
	Source string     `json:"source"`
	Repo   string     `json:"repo,omitempty"`
	From   string     `json:"from"`
	To     string     `json:"to"`
	Days   []DayCount `json:"days"`
	// Repos are the busiest repos in the window, for picking one.
	Repos []RepoCount `json:"repos"`
	// LastPush is the newest push in the repo, inside the window or not,
	// so a repo that went quiet says since when.
	LastPush string `json:"last_push,omitempty"`
}

// timeColumn is the column charts date images by: push time when the
// registry reports it for any image, else the build time from the config.
func (s *Service) timeColumn(ctx context.Context) (col, source string, err error) {
	var n int64
	if err := s.db().QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM pushes LIMIT 1)`).Scan(&n); err != nil {
		return "", "", fmt.Errorf("check push times: %w", err)
	}
	if n > 0 {
		return "pushed", "pushed", nil
	}
	return "CASE WHEN year(created) >= 1980 THEN created END", "created", nil
}

// Activity counts images pushed per day over the last days days, for the
// whole index or one repo, with the busiest repos in that window.
func (s *Service) Activity(ctx context.Context, repo string, days int) (*Activity, error) {
	col, source, err := s.timeColumn(ctx)
	if err != nil {
		return nil, err
	}
	days = clampDays(days, 182)
	out := &Activity{Source: source, Repo: repo, Days: []DayCount{}, Repos: []RepoCount{},
		From: s.cutoff(days - 1), To: s.Now().UTC().Format(time.DateOnly)}
	where, args := `t >= ?::DATE`, []any{out.From}
	if repo != "" {
		where += ` AND repo = ?`
		args = append(args, repo)
	}
	q := `SELECT strftime(t::DATE, '%Y-%m-%d'), count(*) FROM (SELECT repo, ` + col + ` AS t FROM ` + s.view() + `)
		WHERE ` + where + ` GROUP BY 1 ORDER BY 1`
	rows, err := s.db().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("count pushes: %w", err)
	}
	for rows.Next() {
		var d DayCount
		if err := rows.Scan(&d.Day, &d.Count); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out.Days = append(out.Days, d)
	}
	_ = rows.Close()
	rows, err = s.db().QueryContext(ctx, `SELECT repo, count(*) FROM (SELECT repo, `+col+` AS t FROM `+s.view()+`)
		WHERE t >= ?::DATE GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 8`, out.From)
	if err != nil {
		return nil, fmt.Errorf("busiest repos: %w", err)
	}
	for rows.Next() {
		var r RepoCount
		if err := rows.Scan(&r.Repo, &r.Count); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out.Repos = append(out.Repos, r)
	}
	_ = rows.Close()
	if repo != "" {
		var last sql.NullString
		if err := s.db().QueryRowContext(ctx, `SELECT strftime(max(`+col+`)::DATE, '%Y-%m-%d') FROM `+s.view()+` WHERE repo = ?`, repo).Scan(&last); err != nil {
			return nil, fmt.Errorf("last push: %w", err)
		}
		out.LastPush = last.String
	}
	return out, nil
}

// AgeBin is the images in one age range, by verdict.
type AgeBin struct {
	Label    string        `json:"label"`
	Verdicts VerdictCounts `json:"verdicts"`
	// Unrated counts images with no verdict yet.
	Unrated int64 `json:"unrated"`
	// Filter is the query that selects the bin's images.
	Filter string `json:"filter"`
}

// Ages is images by how long ago they were pushed.
type Ages struct {
	Source string   `json:"source"`
	Bins   []AgeBin `json:"bins"`
}

// ageBins are the histogram's ranges, in days: [from, to).
var ageBins = []struct {
	label    string
	from, to int
}{
	{"< 1 week", 0, 7}, {"1–4 weeks", 7, 30}, {"1–3 months", 30, 90}, {"3–6 months", 90, 180},
	{"6–12 months", 180, 365}, {"1–2 years", 365, 730}, {"2+ years", 730, 0},
}

// Ages buckets images by days since their push (or build, where the
// registry does not report pushes), split by staleness verdict.
func (s *Service) Ages(ctx context.Context) (*Ages, error) {
	_, source, err := s.timeColumn(ctx)
	if err != nil {
		return nil, err
	}
	col, field := "pushed_age_days", "pushed_age"
	if source == "created" {
		col, field = "age_days", "age"
	}
	cases := make([]string, 0, len(ageBins))
	out := &Ages{Source: source, Bins: make([]AgeBin, 0, len(ageBins)+1)}
	for i, b := range ageBins {
		cond := fmt.Sprintf("%s >= %d", col, b.from)
		filter := fmt.Sprintf("%s >= %dd", field, b.from)
		if b.from == 0 {
			filter = ""
		}
		if b.to > 0 {
			cond += fmt.Sprintf(" AND %s < %d", col, b.to)
			filter = strings.TrimPrefix(filter+fmt.Sprintf(" and %s < %dd", field, b.to), " and ")
		}
		cases = append(cases, fmt.Sprintf("WHEN %s THEN %d", cond, i))
		out.Bins = append(out.Bins, AgeBin{Label: b.label, Filter: filter})
	}
	out.Bins = append(out.Bins, AgeBin{Label: "unknown", Filter: "not " + field + " exists"})
	rows, err := s.db().QueryContext(ctx, `
		SELECT CASE `+strings.Join(cases, " ")+` ELSE ? END AS bin,
			count(*) FILTER (WHERE stale = 'fresh'), count(*) FILTER (WHERE stale = 'drifting'),
			count(*) FILTER (WHERE stale = 'stale'), count(*) FILTER (WHERE stale = 'abandoned'),
			count(*) FILTER (WHERE stale IS NULL)
		FROM `+s.view()+` GROUP BY 1`, len(ageBins))
	if err != nil {
		return nil, fmt.Errorf("bucket ages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var i int
		var v VerdictCounts
		var unrated int64
		if err := rows.Scan(&i, &v.Fresh, &v.Drifting, &v.Stale, &v.Abandoned, &unrated); err != nil {
			return nil, err
		}
		out.Bins[i].Verdicts, out.Bins[i].Unrated = v, unrated
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if b := out.Bins[len(out.Bins)-1]; b.Unrated+b.Verdicts.Fresh+b.Verdicts.Drifting+b.Verdicts.Stale+b.Verdicts.Abandoned == 0 {
		out.Bins = out.Bins[:len(out.Bins)-1]
	}
	return out, nil
}

// Coverage is how one registry's images were linked to their bases.
type Coverage struct {
	Registry string `json:"registry"`
	Images   int64  `json:"images"`
	// Layers, Provenance and Declared count images whose parent was found
	// by shared layers, by build provenance, or by an annotation or label.
	Layers     int64 `json:"layers"`
	Provenance int64 `json:"provenance"`
	Declared   int64 `json:"declared"`
	// Named counts images whose base is not indexed but is named by their
	// build provenance; Unknown, those whose base is neither.
	Named   int64 `json:"named"`
	Unknown int64 `json:"unknown"`
	// Roots counts images with no parent: bases themselves, FROM scratch
	// and single-layer images.
	Roots int64 `json:"roots"`
}

// BaseCoverage reports, per registry, how many images strata linked to a
// real base and by what evidence.
func (s *Service) BaseCoverage(ctx context.Context) ([]Coverage, error) {
	rows, err := s.db().QueryContext(ctx, `
		WITH missing AS (
			SELECT DISTINCT b.digest FROM base_refs b
			WHERE b.base_digest NOT IN (SELECT digest FROM images)
			  AND b.base_digest NOT IN (SELECT index_digest FROM index_manifests)),
		x AS (
			SELECT split_part(v.repo, '/', 1) AS registry,
				CASE
					WHEN v.parent IS NULL AND (m1.digest IS NOT NULL OR m2.digest IS NOT NULL) THEN 'named'
					WHEN v.parent IS NULL THEN 'root'
					WHEN starts_with(v.parent, 'phantom:') AND (m1.digest IS NOT NULL OR m2.digest IS NOT NULL) THEN 'named'
					WHEN starts_with(v.parent, 'phantom:') THEN 'unknown'
					WHEN v.parent_source = 'attestation' THEN 'provenance'
					WHEN v.parent_source IN ('annotation', 'label') THEN 'declared'
					ELSE 'layers'
				END AS how
			FROM `+s.view()+` v
			LEFT JOIN missing m1 ON m1.digest = v.digest
			LEFT JOIN missing m2 ON m2.digest = v.index_digest)
		SELECT registry, count(*),
			count(*) FILTER (WHERE how = 'layers'), count(*) FILTER (WHERE how = 'provenance'),
			count(*) FILTER (WHERE how = 'declared'), count(*) FILTER (WHERE how = 'named'),
			count(*) FILTER (WHERE how = 'unknown'), count(*) FILTER (WHERE how = 'root')
		FROM x GROUP BY 1 ORDER BY 2 DESC, 1`)
	if err != nil {
		return nil, fmt.Errorf("base coverage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Coverage{}
	for rows.Next() {
		var c Coverage
		if err := rows.Scan(&c.Registry, &c.Images, &c.Layers, &c.Provenance, &c.Declared, &c.Named, &c.Unknown, &c.Roots); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// BaseRepo is a repo other repos build on.
type BaseRepo struct {
	Repo string `json:"repo"`
	// Images counts images in other repos built directly on it.
	Images int64 `json:"images"`
}

// Release is one build of a base: an index or a single-platform image.
type Release struct {
	Digest string   `json:"digest"`
	Tags   []string `json:"tags"`
	Built  string   `json:"built,omitempty"`
	// Behind is how many newer releases of the base exist.
	Behind int `json:"behind"`
	// Images and Repos count images in other repos built directly on this
	// release, and the repos they are in.
	Images   int64         `json:"images"`
	Repos    int64         `json:"repos"`
	Verdicts VerdictCounts `json:"verdicts"`
}

// Adoption is which releases of a base images are built on.
type Adoption struct {
	Base     string     `json:"base"`
	Bases    []BaseRepo `json:"bases"`
	Releases []Release  `json:"releases"`
}

// releaseKey groups a base's platform images into releases.
const releaseKey = `coalesce(b.index_digest, b.digest)`

// BaseRepos lists the repos most built on by other repos.
func (s *Service) BaseRepos(ctx context.Context, limit int) ([]BaseRepo, error) {
	rows, err := s.db().QueryContext(ctx, `
		SELECT b.repo, count(*) FROM `+s.view()+` c JOIN images b ON b.digest = c.parent
		WHERE b.repo <> c.repo GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list base repos: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []BaseRepo{}
	for rows.Next() {
		var b BaseRepo
		if err := rows.Scan(&b.Repo, &b.Images); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Adoption lists a base repo's releases, newest first, with how many
// images in other repos are built directly on each. Releases nothing is
// built on are left out, except the newest. With base empty it picks the
// repo most built on.
func (s *Service) Adoption(ctx context.Context, base string) (*Adoption, error) {
	bases, err := s.BaseRepos(ctx, 15)
	if err != nil {
		return nil, err
	}
	out := &Adoption{Base: base, Bases: bases, Releases: []Release{}}
	if out.Base == "" {
		if len(bases) == 0 {
			return out, nil
		}
		out.Base = bases[0].Repo
	}
	col, _, err := s.timeColumn(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db().QueryContext(ctx, `
		WITH rel AS (
			SELECT `+releaseKey+` AS release, b.digest, b.tag_refs, `+col+` AS t
			FROM `+s.view()+` b WHERE b.repo = ?),
		releases AS (
			SELECT release, list_sort(list_distinct(flatten(list(tag_refs)))) AS tags, max(t) AS t
			FROM rel GROUP BY 1),
		ranked AS (SELECT *, row_number() OVER (ORDER BY t DESC NULLS LAST, release) - 1 AS behind FROM releases),
		kids AS (
			SELECT rel.release, c.repo, c.stale FROM `+s.view()+` c JOIN rel ON rel.digest = c.parent
			WHERE c.repo <> ?)
		SELECT r.release, any_value(r.tags), coalesce(strftime(any_value(r.t)::DATE, '%Y-%m-%d'), ''), any_value(r.behind),
			count(k.repo), count(DISTINCT k.repo),
			count(*) FILTER (WHERE k.stale = 'fresh'), count(*) FILTER (WHERE k.stale = 'drifting'),
			count(*) FILTER (WHERE k.stale = 'stale'), count(*) FILTER (WHERE k.stale = 'abandoned')
		FROM ranked r LEFT JOIN kids k ON k.release = r.release
		GROUP BY r.release HAVING count(k.repo) > 0 OR any_value(r.behind) = 0
		ORDER BY any_value(r.behind) LIMIT 40`, out.Base, out.Base)
	if err != nil {
		return nil, fmt.Errorf("base adoption: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var r Release
		var tags any
		if err := rows.Scan(&r.Digest, &tags, &r.Built, &r.Behind, &r.Images, &r.Repos,
			&r.Verdicts.Fresh, &r.Verdicts.Drifting, &r.Verdicts.Stale, &r.Verdicts.Abandoned); err != nil {
			return nil, err
		}
		r.Tags = toStrings(tags)
		out.Releases = append(out.Releases, r)
	}
	return out, rows.Err()
}

// RolloutSeries is one release's image count per day.
type RolloutSeries struct {
	Release string   `json:"release"`
	Tags    []string `json:"tags"`
	Built   string   `json:"built,omitempty"`
	// Older is true for the series that folds older and removed releases.
	Older  bool    `json:"older,omitempty"`
	Counts []int64 `json:"counts"`
}

// Rollout is how images built on a base moved between its releases.
type Rollout struct {
	Base   string          `json:"base"`
	Days   []string        `json:"days"`
	Series []RolloutSeries `json:"series"`
}

// rolloutReleases is how many releases a rollout charts on their own.
const rolloutReleases = 4

// Rollout returns, per day over the last days days, how many images in
// other repos were built directly on each release of a base: the newest
// releases on their own, older and deleted ones together. It reads the
// parent counts snapshots record, so it starts with the first snapshot
// that recorded them.
func (s *Service) Rollout(ctx context.Context, base string, days int) (*Rollout, error) {
	if base == "" {
		return nil, input(fmt.Errorf("name a base repo"))
	}
	col, _, err := s.timeColumn(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db().QueryContext(ctx, `
		WITH counts AS (
			SELECT s.day, s.group_value AS parent, s.value FROM snapshots s
			WHERE s.group_by = 'parent' AND s.metric = 'children' AND s.day >= ?::DATE),
		mine AS (
			SELECT `+releaseKey+` AS release, b.digest, b.tag_refs, `+col+` AS t
			FROM `+s.view()+` b WHERE b.repo = ?),
		releases AS (
			SELECT release, list_sort(list_distinct(flatten(list(tag_refs)))) AS tags, max(t) AS t
			FROM mine GROUP BY 1),
		recent AS (
			SELECT r.* FROM releases r WHERE r.release IN (
				SELECT m.release FROM counts c JOIN mine m ON m.digest = c.parent)
			ORDER BY r.t DESC NULLS LAST, r.release LIMIT ?)
		SELECT strftime(c.day, '%Y-%m-%d'), r.release, any_value(r.tags), coalesce(strftime(any_value(r.t)::DATE, '%Y-%m-%d'), ''),
			sum(c.value)::BIGINT
		FROM counts c JOIN mine m ON m.digest = c.parent LEFT JOIN recent r ON r.release = m.release
		GROUP BY c.day, r.release ORDER BY 1`, s.cutoff(clampDays(days, 60)), base, rolloutReleases)
	if err != nil {
		return nil, fmt.Errorf("base rollout: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := &Rollout{Base: base, Days: []string{}, Series: []RolloutSeries{}}
	byRelease := map[string]*RolloutSeries{}
	type cell struct {
		key string
		day int
		n   int64
	}
	var cells []cell
	for rows.Next() {
		var day, built string
		var release sql.NullString
		var tags any
		var n int64
		if err := rows.Scan(&day, &release, &tags, &built, &n); err != nil {
			return nil, err
		}
		if len(out.Days) == 0 || out.Days[len(out.Days)-1] != day {
			out.Days = append(out.Days, day)
		}
		key := release.String
		if _, ok := byRelease[key]; !ok {
			ser := &RolloutSeries{Release: key, Tags: toStrings(tags), Built: built, Older: !release.Valid}
			if ser.Older {
				ser.Release = "older"
			}
			byRelease[key] = ser
		}
		cells = append(cells, cell{key, len(out.Days) - 1, n})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, ser := range byRelease {
		ser.Counts = make([]int64, len(out.Days))
	}
	for _, c := range cells {
		byRelease[c.key].Counts[c.day] += c.n
	}
	for _, ser := range byRelease {
		out.Series = append(out.Series, *ser)
	}
	// Newest release first; older last.
	sort.Slice(out.Series, func(i, j int) bool {
		a, b := out.Series[i], out.Series[j]
		if a.Older != b.Older {
			return b.Older
		}
		if a.Built != b.Built {
			return a.Built > b.Built
		}
		return a.Release < b.Release
	})
	return out, nil
}

// parentCounts are the snapshot rows behind Rollout: for every indexed
// image that images in other repos are built directly on, how many are.
func (s *Service) parentCounts(ctx context.Context, date time.Time) ([][]any, error) {
	rows, err := s.db().QueryContext(ctx, `
		SELECT c.parent, count(*) FROM `+s.view()+` c JOIN images b ON b.digest = c.parent
		WHERE b.repo <> c.repo GROUP BY 1`)
	if err != nil {
		return nil, fmt.Errorf("count images per parent: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out [][]any
	for rows.Next() {
		var parent string
		var n int64
		if err := rows.Scan(&parent, &n); err != nil {
			return nil, err
		}
		out = append(out, []any{date, "parent", parent, "children", float64(n)})
	}
	return out, rows.Err()
}
