package stale

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"

	"github.com/rossedman/strata/internal/model"
)

// Facts are what the index knows about one image, as input to Evaluate.
type Facts struct {
	Digest      string
	IndexDigest string
	Repo        string
	Created     time.Time
	Tags        int
	Descendants int
	Labels      map[string]string
	RootRefs    []string
	// LastPulled is the last pull time, or zero if there is no pull data.
	LastPulled time.Time
	// Parent describes the image's preferred parent, if it has one.
	Parent *ParentFacts
}

// ParentFacts describe an image's parent for base-drift detection.
type ParentFacts struct {
	Digest      string
	IndexDigest string
	Repo        string
	Created     time.Time
	Phantom     bool
}

// History is tag history grouped by repo:tag, each list oldest first.
type History map[string][]model.TagEvent

// NewHistory groups and sorts tag events.
func NewHistory(events []model.TagEvent) History {
	h := History{}
	for _, e := range events {
		k := e.Repo + ":" + e.Tag
		h[k] = append(h[k], e)
	}
	for _, list := range h {
		sort.SliceStable(list, func(i, j int) bool { return list[i].SeenAt.Before(list[j].SeenAt) })
	}
	return h
}

// move describes a tag that once held a digest and moved on.
type move struct {
	ref         string
	generations int
	replacedAt  time.Time
	lastMoveAt  time.Time
}

// movesAway finds every tag that once pointed at one of digests, and how
// it moved on since: the number of new digests it took, when it first
// left and when it last moved. A tag that still points at the digest
// reports zero generations. Deleted tags are skipped.
func (h History) movesAway(digests ...string) []move {
	held := func(d string) bool {
		for _, x := range digests {
			if x != "" && d == x {
				return true
			}
		}
		return false
	}
	var out []move
	for ref, events := range h {
		last := -1
		for i, e := range events {
			if held(e.Digest) {
				last = i
			}
		}
		if last < 0 {
			continue
		}
		m := move{ref: ref}
		prev := events[last].Digest
		deleted := false
		for _, e := range events[last+1:] {
			if e.Digest == "" {
				deleted = true
				break
			}
			if e.Digest == prev {
				continue
			}
			if m.generations == 0 {
				m.replacedAt = e.SeenAt
			}
			m.generations++
			m.lastMoveAt = e.SeenAt
			prev = e.Digest
		}
		if !deleted {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ref < out[j].ref })
	return out
}

// RepoImages lists the creation times of the images in each repo, for
// approximating drift before tag history began.
type RepoImages map[string][]time.Time

// newerThan counts images in repo created after t.
func (r RepoImages) newerThan(repo string, t time.Time) int {
	n := 0
	for _, c := range r[repo] {
		if c.After(t) {
			n++
		}
	}
	return n
}

// Result is a verdict with the signals behind it.
type Result struct {
	Digest               string   `json:"digest"`
	Verdict              string   `json:"verdict"`
	Reasons              []string `json:"reasons"`
	BaseDriftGenerations int      `json:"base_drift_generations"`
	BaseDriftDays        *int     `json:"base_drift_days,omitempty"`
	BaseTag              string   `json:"base_tag,omitempty"`
	SupersededDays       *int     `json:"superseded_days,omitempty"`
	Untagged             bool     `json:"untagged"`
	AgeDays              *int     `json:"age_days,omitempty"`
	LastPulledDays       *int     `json:"last_pulled_days,omitempty"`
	EOLBase              bool     `json:"eol_base"`
	LabelPolicy          bool     `json:"label_policy"`
	Inferred             bool     `json:"inferred"`
}

// Evaluator turns facts into verdicts under one policy.
type Evaluator struct {
	Policy  Policy
	History History
	Repos   RepoImages
	Now     time.Time
	// HavePulls is whether any pull data exists in the index.
	HavePulls bool
}

// days returns whole days from t to the evaluator's now.
func (e *Evaluator) days(t time.Time) int {
	return int(e.Now.Sub(t).Hours() / 24)
}

// Evaluate computes the verdict and reasons for one image.
func (e *Evaluator) Evaluate(f Facts) Result {
	r := Result{Digest: f.Digest, Reasons: []string{}}
	th := e.Policy.For(f.Repo, f.Labels)
	e.baseDrift(f, &r)
	e.superseded(f, &r)
	r.Untagged = f.Tags == 0
	if !f.Created.IsZero() && f.Created.Year() >= 1980 {
		d := e.days(f.Created)
		r.AgeDays = &d
	}
	if !f.LastPulled.IsZero() {
		d := e.days(f.LastPulled)
		r.LastPulledDays = &d
	}
	if ref, ok := globMatch(e.Policy.EOLRoots, f.RootRefs); ok {
		r.EOLBase = true
		r.Reasons = append(r.Reasons, fmt.Sprintf("root base %s is past end of life", ref))
	}
	e.labelPolicy(f, &r)

	unpulled := r.LastPulledDays == nil && !e.HavePulls && e.Policy.AssumeUnpulled != nil && *e.Policy.AssumeUnpulled
	if r.LastPulledDays != nil && *r.LastPulledDays > th.AbandonedPullDays {
		unpulled = true
	}
	switch {
	case r.Untagged && f.Descendants == 0 && unpulled:
		r.Verdict = Abandoned
		why := fmt.Sprintf("untagged, no descendants and no pulls in %d days", th.AbandonedPullDays)
		if r.LastPulledDays == nil {
			why = "untagged and no descendants (no pull data)"
		}
		r.Reasons = append([]string{why}, r.Reasons...)
	case r.BaseDriftGenerations >= th.StaleGenerations,
		r.SupersededDays != nil && *r.SupersededDays > th.SupersededDays,
		r.EOLBase, r.LabelPolicy:
		r.Verdict = Stale
	case r.BaseDriftGenerations > 0, r.SupersededDays != nil:
		r.Verdict = Drifting
	default:
		r.Verdict = Fresh
		if r.Untagged {
			r.Reasons = append(r.Reasons, "untagged, but other images are built on it")
		}
	}
	return r
}

// baseDrift fills in whether the image's parent has been replaced under
// its tag, preferring the tag it has drifted least on.
func (e *Evaluator) baseDrift(f Facts, r *Result) {
	p := f.Parent
	if p == nil {
		return
	}
	if p.Phantom {
		// A phantom is a shared prefix with no image: usually an external
		// base that was never mirrored, sometimes a deleted one. Neither
		// says anything about drift.
		return
	}
	moves := e.History.movesAway(p.Digest, p.IndexDigest)
	if len(moves) == 0 {
		// No history covers the parent. If a newer image exists in the
		// parent's repo, the parent was likely superseded.
		if p.Created.IsZero() {
			return
		}
		if n := e.Repos.newerThan(p.Repo, p.Created); n > 0 {
			r.BaseDriftGenerations = 1
			r.BaseTag = p.Repo
			r.Inferred = true
			r.Reasons = append(r.Reasons, fmt.Sprintf("base %s has %d newer image(s) than this image's parent (inferred, no tag history)", p.Repo, n))
		}
		return
	}
	best := moves[0]
	for _, m := range moves[1:] {
		if m.generations < best.generations {
			best = m
		}
	}
	r.BaseTag = best.ref
	if best.generations == 0 {
		return
	}
	r.BaseDriftGenerations = best.generations
	d := e.days(best.replacedAt)
	r.BaseDriftDays = &d
	times := "times"
	if best.generations == 1 {
		times = "time"
	}
	r.Reasons = append(r.Reasons, fmt.Sprintf("base %s moved %d %s since this image's parent (last move %d days ago)",
		best.ref, best.generations, times, e.days(best.lastMoveAt)))
}

// superseded fills in whether a tag that held this image now holds a
// newer one, using the longest-superseded tag.
func (e *Evaluator) superseded(f Facts, r *Result) {
	var worst *move
	for _, m := range e.History.movesAway(f.Digest, f.IndexDigest) {
		if m.generations == 0 {
			continue
		}
		if worst == nil || m.replacedAt.Before(worst.replacedAt) {
			mm := m
			worst = &mm
		}
	}
	if worst == nil {
		return
	}
	d := e.days(worst.replacedAt)
	r.SupersededDays = &d
	r.Reasons = append(r.Reasons, fmt.Sprintf("superseded under %s %d days ago", worst.ref, d))
}

// labelPolicy flags images whose own metadata marks them deprecated or
// expired.
func (e *Evaluator) labelPolicy(f Facts, r *Result) {
	for _, k := range e.Policy.DeprecatedLabels {
		switch strings.ToLower(f.Labels[k]) {
		case "true", "yes", "1":
			r.LabelPolicy = true
			r.Reasons = append(r.Reasons, fmt.Sprintf("label %s=%s", k, f.Labels[k]))
		}
	}
	for _, k := range e.Policy.ExpiresLabels {
		v := f.Labels[k]
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			t, err = time.Parse(time.DateOnly, v)
		}
		if err == nil && t.Before(e.Now) {
			r.LabelPolicy = true
			r.Reasons = append(r.Reasons, fmt.Sprintf("label %s=%s is in the past", k, v))
		}
	}
}

// Recompute evaluates every image in the index and replaces the staleness
// table.
func Recompute(ctx context.Context, db *sql.DB, policy Policy, now time.Time) error {
	ev, facts, err := load(ctx, db, policy, now)
	if err != nil {
		return err
	}
	results := make([]Result, len(facts))
	for i, f := range facts {
		results[i] = ev.Evaluate(f)
	}
	return write(ctx, db, results, now)
}

// load reads tag history, repo creation times and per-image facts.
func load(ctx context.Context, db *sql.DB, policy Policy, now time.Time) (*Evaluator, []Facts, error) {
	ev := &Evaluator{Policy: policy, Now: now, Repos: RepoImages{}}
	events, err := queryEvents(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	ev.History = NewHistory(events)
	if err := db.QueryRowContext(ctx, `SELECT count(*) > 0 FROM pulls`).Scan(&ev.HavePulls); err != nil {
		return nil, nil, fmt.Errorf("read pulls: %w", err)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT v.digest, coalesce(v.index_digest, ''), v.repo, v.created, v.tags, v.descendants,
			coalesce(v.labels::JSON::VARCHAR, '{}'), v.root_refs,
			greatest(pl.last_pulled, plx.last_pulled),
			coalesce(v.parent, ''), coalesce(p.index_digest, ''), coalesce(p.repo, ''), p.created
		FROM image_view v LEFT JOIN images p ON p.digest = v.parent
		LEFT JOIN (SELECT digest, max(last_pulled) AS last_pulled FROM pulls GROUP BY digest) pl ON pl.digest = v.digest
		LEFT JOIN (SELECT digest, max(last_pulled) AS last_pulled FROM pulls GROUP BY digest) plx ON plx.digest = v.index_digest`)
	if err != nil {
		return nil, nil, fmt.Errorf("read image facts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var facts []Facts
	for rows.Next() {
		var f Facts
		var created, pulled, parentCreated sql.NullTime
		var labels string
		var rootRefs []any
		var parent, parentIndex, parentRepo string
		if err := rows.Scan(&f.Digest, &f.IndexDigest, &f.Repo, &created, &f.Tags, &f.Descendants,
			&labels, &rootRefs, &pulled, &parent, &parentIndex, &parentRepo, &parentCreated); err != nil {
			return nil, nil, fmt.Errorf("scan image facts: %w", err)
		}
		f.Created = created.Time
		f.LastPulled = pulled.Time
		f.Labels = map[string]string{}
		var raw map[string]any
		if err := json.Unmarshal([]byte(labels), &raw); err == nil {
			for k, v := range raw {
				f.Labels[k] = fmt.Sprint(v)
			}
		}
		for _, r := range rootRefs {
			f.RootRefs = append(f.RootRefs, fmt.Sprint(r))
		}
		if parent != "" {
			f.Parent = &ParentFacts{Digest: parent, IndexDigest: parentIndex, Repo: parentRepo,
				Created: parentCreated.Time, Phantom: model.IsPhantom(parent)}
		}
		ev.Repos[f.Repo] = append(ev.Repos[f.Repo], f.Created)
		facts = append(facts, f)
	}
	return ev, facts, rows.Err()
}

// queryEvents reads all of tag history.
func queryEvents(ctx context.Context, db *sql.DB) ([]model.TagEvent, error) {
	rows, err := db.QueryContext(ctx, `SELECT repo, tag, seen_at, coalesce(digest, ''), source FROM tag_history`)
	if err != nil {
		return nil, fmt.Errorf("read tag history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []model.TagEvent
	for rows.Next() {
		var e model.TagEvent
		if err := rows.Scan(&e.Repo, &e.Tag, &e.SeenAt, &e.Digest, &e.Source); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// write replaces the staleness table with results.
func write(ctx context.Context, db *sql.DB, results []Result, now time.Time) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN TRANSACTION; DELETE FROM staleness"); err != nil {
		return fmt.Errorf("clear staleness: %w", err)
	}
	err = conn.Raw(func(dc any) error { return appendResults(dc.(driver.Conn), results, now) })
	if err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit staleness: %w", err)
	}
	return nil
}

// appendResults bulk-loads results, staging reasons as JSON text.
func appendResults(conn driver.Conn, results []Result, now time.Time) error {
	types := []duckdb.Type{
		duckdb.TYPE_VARCHAR, duckdb.TYPE_VARCHAR, duckdb.TYPE_VARCHAR, duckdb.TYPE_INTEGER, duckdb.TYPE_INTEGER,
		duckdb.TYPE_VARCHAR, duckdb.TYPE_INTEGER, duckdb.TYPE_BOOLEAN, duckdb.TYPE_INTEGER, duckdb.TYPE_INTEGER,
		duckdb.TYPE_BOOLEAN, duckdb.TYPE_BOOLEAN, duckdb.TYPE_BOOLEAN, duckdb.TYPE_TIMESTAMP_TZ,
	}
	infos := make([]duckdb.TypeInfo, len(types))
	for i, t := range types {
		ti, err := duckdb.NewTypeInfo(t)
		if err != nil {
			return err
		}
		infos[i] = ti
	}
	a, err := duckdb.NewQueryAppender(conn, `INSERT INTO staleness SELECT col1, col2, col3::JSON::VARIANT,
		col4, col5, col6, col7, col8, col9, col10, col11, col12, col13, col14 FROM appended_data`, "", infos, nil)
	if err != nil {
		return fmt.Errorf("appender for staleness: %w", err)
	}
	for _, r := range results {
		reasons, _ := json.Marshal(r.Reasons)
		if err := a.AppendRow(r.Digest, r.Verdict, string(reasons), clamp32(r.BaseDriftGenerations),
			intOrNil(r.BaseDriftDays), r.BaseTag, intOrNil(r.SupersededDays), r.Untagged, intOrNil(r.AgeDays),
			intOrNil(r.LastPulledDays), r.EOLBase, r.LabelPolicy, r.Inferred, now); err != nil {
			_ = a.Close()
			return fmt.Errorf("append staleness: %w", err)
		}
	}
	if err := a.Close(); err != nil {
		return fmt.Errorf("flush staleness: %w", err)
	}
	return nil
}

// intOrNil converts an optional int to a driver value.
func intOrNil(p *int) any {
	if p == nil {
		return nil
	}
	return clamp32(*p)
}

// clamp32 converts an int to int32, saturating at the type's bounds.
func clamp32(n int) int32 {
	n = max(math.MinInt32, min(math.MaxInt32, n))
	return int32(n) //nolint:gosec // clamped to the int32 range above
}
