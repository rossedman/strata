package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/rossedman/strata/internal/query"
)

// DefaultCleanupFilter selects deletion candidates when none is given:
// images the staleness policy calls abandoned, which are untagged, have
// nothing built on them and have not been pulled in the policy's window.
const DefaultCleanupFilter = `stale = "abandoned"`

// Cleanup scopes: which images' layers count as kept when working out
// what deleting the candidates frees.
const (
	// ScopeRegistry counts a layer as freed only when no kept image in
	// any repo uses it, as for a registry that stores each blob once.
	ScopeRegistry = "registry"
	// ScopeRepo counts a layer as freed when no kept image in the same
	// repo uses it, as for a registry that stores blobs per repository.
	ScopeRepo = "repo"
)

// CleanupOptions pick deletion candidates.
type CleanupOptions struct {
	// Where selects candidates with the query language; empty means
	// DefaultCleanupFilter.
	Where string
	// Scope is ScopeRegistry (the default) or ScopeRepo.
	Scope string
	// Limit caps the candidates listed; totals count them all. Zero
	// lists them all.
	Limit int
}

// CleanupCandidate is one manifest, or one multi-platform index with its
// platform images, that can be deleted.
type CleanupCandidate struct {
	// Digest is what to delete: the manifest, or the index.
	Digest string `json:"digest"`
	Repo   string `json:"repo"`
	// Images counts the platform images it holds; 1 for a manifest.
	Images int `json:"images"`
	// Size is the compressed size its manifests state.
	Size int64 `json:"size"`
	// Exclusive is what deleting only this candidate frees: layers no
	// other image in scope uses, candidates included.
	Exclusive      int64  `json:"exclusive_bytes"`
	Verdict        string `json:"verdict"`
	Reason         string `json:"reason"`
	PushedDays     *int   `json:"pushed_days"`
	LastPulledDays *int   `json:"last_pulled_days"`
}

// CleanupRepo totals the candidates of one repo.
type CleanupRepo struct {
	Repo        string `json:"repo"`
	Candidates  int    `json:"candidates"`
	Size        int64  `json:"size"`
	Reclaimable int64  `json:"reclaimable_bytes"`
}

// Cleanup is the list of deletion candidates and what deleting them all
// frees.
type Cleanup struct {
	Where string `json:"where"`
	Scope string `json:"scope"`
	// Candidates are ordered by exclusive bytes, largest first.
	Candidates []CleanupCandidate `json:"candidates"`
	Repos      []CleanupRepo      `json:"repos"`
	// Total counts candidates; Images counts their platform images.
	Total  int `json:"total"`
	Images int `json:"images"`
	// Size is the sum of the candidates' stated sizes, which counts a
	// shared layer once per image; Reclaimable counts each freed layer
	// once, and leaves out layers a kept image still uses.
	Size        int64 `json:"size"`
	Reclaimable int64 `json:"reclaimable_bytes"`
	// Held counts images that matched but are kept: other images are
	// built on them, or they share an index with images that are kept.
	Held int `json:"held"`
}

// CleanupColumns are the columns of a candidate export, in order.
var CleanupColumns = []string{"repo", "digest", "images", "size", "exclusive_bytes", "verdict", "pushed_days", "last_pulled_days", "reason"}

// Row returns the candidate as values in CleanupColumns order.
func (c CleanupCandidate) Row() []any {
	return []any{c.Repo, c.Digest, int64(c.Images), c.Size, c.Exclusive, c.Verdict, intOrNil(c.PushedDays), intOrNil(c.LastPulledDays), c.Reason}
}

// cleanupScope returns the partition a layer is shared within.
func cleanupScope(scope string) (string, string, error) {
	switch scope {
	case "", ScopeRegistry:
		return ScopeRegistry, "''", nil
	case ScopeRepo:
		return ScopeRepo, "i.repo", nil
	}
	return "", "", input(fmt.Errorf("scope %q: use registry or repo", scope))
}

// Cleanup lists deletion candidates: images matching the filter (abandoned
// ones by default), grouped into whole multi-platform indexes, leaving out
// any that a kept image is built on, with the storage deleting them frees
// after accounting for layers shared with kept images.
func (s *Service) Cleanup(ctx context.Context, opts CleanupOptions) (*Cleanup, error) {
	where := opts.Where
	if strings.TrimSpace(where) == "" {
		where = DefaultCleanupFilter
	}
	scope, part, err := cleanupScope(opts.Scope)
	if err != nil {
		return nil, err
	}
	c, err := query.Compile(where)
	if err != nil {
		return nil, input(err)
	}
	conn, err := s.db().Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	exec := func(q string, args ...any) (int64, error) {
		res, err := conn.ExecContext(ctx, q, args...)
		if err != nil {
			return 0, fmt.Errorf("cleanup: %w", err)
		}
		return res.RowsAffected()
	}
	// cand holds candidate images and the unit (index or manifest) they
	// are deleted with. A unit is a candidate only when every platform
	// image in it matches.
	if _, err := exec(`CREATE OR REPLACE TEMP TABLE cleanup_matched AS
		SELECT digest FROM `+s.view()+` WHERE NOT external AND (`+c.Where+`)`, c.Args...); err != nil {
		return nil, err
	}
	if _, err := exec(`CREATE OR REPLACE TEMP TABLE cleanup_cand AS
		WITH img AS (
			SELECT digest, repo, coalesce(index_digest, digest) AS unit FROM images WHERE NOT coalesce(external, false)
		), whole AS (
			SELECT unit FROM img GROUP BY unit HAVING bool_and(digest IN (SELECT digest FROM cleanup_matched))
		)
		SELECT digest, repo, unit FROM img WHERE unit IN (SELECT unit FROM whole)`); err != nil {
		return nil, err
	}
	var matched int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM cleanup_matched`).Scan(&matched); err != nil {
		return nil, err
	}
	// Anything a kept image is built on is kept, with its whole unit;
	// keeping a unit can keep its ancestors in turn.
	for {
		n, err := exec(`DELETE FROM cleanup_cand WHERE unit IN (
			WITH RECURSIVE up(id) AS (
				SELECT l.parent FROM lineage l JOIN images i ON i.digest = l.digest
				WHERE l.parent IS NOT NULL AND l.digest NOT IN (SELECT digest FROM cleanup_cand)
				UNION SELECT l.parent FROM lineage l JOIN up ON l.digest = up.id WHERE l.parent IS NOT NULL
			) SELECT c.unit FROM cleanup_cand c JOIN up ON up.id = c.digest)`)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			break
		}
	}
	// Layers each candidate unit uses, and whether a kept image or
	// another unit in the same partition uses them too.
	if _, err := exec(`CREATE OR REPLACE TEMP TABLE cleanup_layers AS
		WITH used AS (
			SELECT DISTINCT ` + part + ` AS part, il.blob_digest AS blob, il.size, c.unit
			FROM image_layers il JOIN images i ON i.digest = il.digest
			LEFT JOIN cleanup_cand c ON c.digest = il.digest
			WHERE NOT coalesce(i.external, false)
		)
		SELECT part, blob, any_value(size) AS size, list(DISTINCT unit) FILTER (WHERE unit IS NOT NULL) AS units,
			bool_or(unit IS NULL) AS kept
		FROM used GROUP BY part, blob`); err != nil {
		return nil, err
	}
	out := &Cleanup{Where: where, Scope: scope, Candidates: []CleanupCandidate{}, Repos: []CleanupRepo{}}
	if err := conn.QueryRowContext(ctx, `SELECT
			(SELECT count(DISTINCT unit) FROM cleanup_cand), (SELECT count(*) FROM cleanup_cand),
			(SELECT coalesce(sum(i.size), 0) FROM cleanup_cand c JOIN images i ON i.digest = c.digest),
			(SELECT coalesce(sum(size), 0) FROM cleanup_layers WHERE NOT kept AND len(units) > 0)`).
		Scan(&out.Total, &out.Images, &out.Size, &out.Reclaimable); err != nil {
		return nil, err
	}
	out.Held = matched - out.Images
	//nolint:gosec // s.view() names a fixed table
	q := `WITH unit AS (
			SELECT c.unit, any_value(c.repo) AS repo, count(*) AS images, sum(i.size) AS size,
				any_value(v.stale) AS verdict, coalesce(any_value(v.stale_reasons)[1]::VARCHAR, '') AS reason,
				max(v.pushed_age_days) AS pushed_days, min(v.stale_last_pulled_days) AS pulled_days
			FROM cleanup_cand c JOIN images i ON i.digest = c.digest JOIN ` + s.view() + ` v ON v.digest = c.digest
			GROUP BY c.unit
		), excl AS (
			SELECT units[1] AS unit, sum(size) AS bytes FROM cleanup_layers WHERE NOT kept AND len(units) = 1 GROUP BY 1
		)
		SELECT u.unit, u.repo, u.images, u.size, coalesce(e.bytes, 0), coalesce(u.verdict, ''), trim(u.reason, '"'), u.pushed_days, u.pulled_days
		FROM unit u LEFT JOIN excl e ON e.unit = u.unit
		ORDER BY 5 DESC, u.size DESC, u.repo, u.unit`
	var args []any
	if opts.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, opts.Limit)
	}
	rows, err := conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list cleanup candidates: %w", err)
	}
	for rows.Next() {
		var cc CleanupCandidate
		var pushed, pulled sql.NullInt64
		if err := rows.Scan(&cc.Digest, &cc.Repo, &cc.Images, &cc.Size, &cc.Exclusive, &cc.Verdict, &cc.Reason, &pushed, &pulled); err != nil {
			_ = rows.Close()
			return nil, err
		}
		cc.PushedDays, cc.LastPulledDays = nullInt(pushed), nullInt(pulled)
		out.Candidates = append(out.Candidates, cc)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Per repo, a layer is freed when no kept image in the partition
	// uses it and every candidate using it is in the repo.
	rows, err = conn.QueryContext(ctx, `WITH per AS (
			SELECT c.repo, count(DISTINCT c.unit) AS candidates, sum(i.size) AS size
			FROM cleanup_cand c JOIN images i ON i.digest = c.digest GROUP BY c.repo
		), freed AS (
			SELECT any_value(c.repo) AS repo, l.blob, any_value(l.size) AS size
			FROM cleanup_layers l, unnest(l.units) AS u(unit) JOIN cleanup_cand c ON c.unit = u.unit
			WHERE NOT l.kept GROUP BY l.part, l.blob HAVING count(DISTINCT c.repo) = 1
		)
		SELECT p.repo, p.candidates, p.size, coalesce((SELECT sum(size) FROM freed f WHERE f.repo = p.repo), 0) AS bytes
		FROM per p ORDER BY bytes DESC, p.repo`)
	if err != nil {
		return nil, fmt.Errorf("total cleanup by repo: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var r CleanupRepo
		if err := rows.Scan(&r.Repo, &r.Candidates, &r.Size, &r.Reclaimable); err != nil {
			return nil, err
		}
		out.Repos = append(out.Repos, r)
	}
	return out, rows.Err()
}

// LifecycleOptions shape generated ECR lifecycle policies.
type LifecycleOptions struct {
	// Repos limits the repos, as globs over the repo name with its host.
	Repos []string
	// Days overrides the age untagged images expire at; zero uses each
	// repo's abandoned_pull_days from the staleness policy.
	Days int
	// IncludeConflicts keeps a repo's rule even when it would expire
	// images strata keeps.
	IncludeConflicts bool
}

// Lifecycle is a generated ECR lifecycle policy for one repo, checked
// against the index.
type Lifecycle struct {
	Repo string `json:"repo"`
	// Name is the repository name ECR knows it by, without the host.
	Name string `json:"name"`
	Days int    `json:"days"`
	// Expires counts the images the rule would expire today; Agree counts
	// those strata also lists for cleanup.
	Expires int `json:"expires"`
	Agree   int `json:"agree"`
	// Conflicts are images the rule would expire that strata keeps, with
	// why; a repo with conflicts gets no policy unless asked.
	Conflicts []LifecycleConflict `json:"conflicts"`
	// Policy is the lifecycle policy document, or nil when the repo is
	// skipped.
	Policy *LifecyclePolicy `json:"policy"`
	// Skipped says why the repo has no policy.
	Skipped string `json:"skipped,omitempty"`
}

// LifecycleConflict is an image a rule would expire that strata keeps.
type LifecycleConflict struct {
	Digest string `json:"digest"`
	Reason string `json:"reason"`
}

// LifecyclePolicy is an ECR lifecycle policy document.
type LifecyclePolicy struct {
	Rules []LifecycleRule `json:"rules"`
}

// LifecycleRule is one rule of an ECR lifecycle policy.
type LifecycleRule struct {
	RulePriority int                `json:"rulePriority"`
	Description  string             `json:"description"`
	Selection    LifecycleSelection `json:"selection"`
	Action       LifecycleAction    `json:"action"`
}

// LifecycleSelection picks the images a rule applies to.
type LifecycleSelection struct {
	TagStatus   string `json:"tagStatus"`
	CountType   string `json:"countType"`
	CountUnit   string `json:"countUnit,omitempty"`
	CountNumber int    `json:"countNumber"`
}

// LifecycleAction is what a rule does.
type LifecycleAction struct {
	Type string `json:"type"`
}

// Text returns the policy as the JSON put-lifecycle-policy takes.
func (p *LifecyclePolicy) Text() string {
	b, _ := json.Marshal(p)
	return string(b)
}

// maxConflicts caps the conflicts listed per repo.
const maxConflicts = 20

// Lifecycles generates an ECR lifecycle policy per repo that expires
// untagged images once they are older than the staleness policy's
// abandoned window, the closest rule ECR can enforce to strata's
// abandoned verdict: ECR can see push age but not pulls or what is built
// on an image. Each rule is checked against the index, and a repo where
// it would expire an image strata keeps is skipped and says why.
func (s *Service) Lifecycles(ctx context.Context, opts LifecycleOptions) ([]Lifecycle, error) {
	for _, g := range opts.Repos {
		if _, err := path.Match(g, ""); err != nil {
			return nil, input(fmt.Errorf("repo glob %q: %w", g, err))
		}
	}
	if opts.Days < 0 {
		return nil, input(fmt.Errorf("days must not be negative"))
	}
	// The rule agrees with strata where it expires an image the cleanup
	// report lists, whole index included.
	cl, err := s.Cleanup(ctx, CleanupOptions{})
	if err != nil {
		return nil, err
	}
	listed := map[string]bool{}
	for _, c := range cl.Candidates {
		listed[c.Digest] = true
	}
	// Every manifest ECR sees as untagged, by repo: one with no tag of
	// its own. Its push age comes from the registry's API, or from when
	// strata first saw it.
	rows, err := s.db().QueryContext(ctx, `
		SELECT i.repo, i.digest, coalesce(i.index_digest, i.digest), s.verdict = 'abandoned', coalesce(date_diff('day', p.pushed_at, ?::TIMESTAMPTZ), date_diff('day', i.indexed_at, ?::TIMESTAMPTZ)),
			i.index_digest IS NOT NULL AND EXISTS (SELECT 1 FROM tags t WHERE t.digest = i.index_digest),
			coalesce(l.descendants, 0), s.last_pulled_days
		FROM images i
		LEFT JOIN pushes p ON p.repo = i.repo AND p.digest = i.digest
		LEFT JOIN lineage l ON l.digest = i.digest
		LEFT JOIN staleness s ON s.digest = i.digest
		WHERE NOT coalesce(i.external, false) AND NOT EXISTS (SELECT 1 FROM tags t WHERE t.digest = i.digest)
		UNION ALL
		SELECT repo, NULL, NULL, NULL, NULL, NULL, NULL, NULL FROM images WHERE NOT coalesce(external, false)
		ORDER BY 1, 2`, s.Now(), s.Now())
	if err != nil {
		return nil, fmt.Errorf("read untagged images: %w", err)
	}
	defer func() { _ = rows.Close() }()
	byRepo := map[string]*Lifecycle{}
	var order []string
	for rows.Next() {
		var repo string
		var digest, unit sql.NullString
		var age, desc, pulled sql.NullInt64
		var abandoned, inIndex sql.NullBool
		if err := rows.Scan(&repo, &digest, &unit, &abandoned, &age, &inIndex, &desc, &pulled); err != nil {
			return nil, err
		}
		if !repoMatches(repo, opts.Repos) {
			continue
		}
		lc := byRepo[repo]
		if lc == nil {
			days := opts.Days
			if days == 0 {
				days = s.Policy.For(repo, nil).AbandonedPullDays
			}
			_, name, _ := strings.Cut(repo, "/")
			lc = &Lifecycle{Repo: repo, Name: name, Days: max(days, 1), Conflicts: []LifecycleConflict{}}
			byRepo[repo] = lc
			order = append(order, repo)
		}
		if !digest.Valid || int(age.Int64) <= lc.Days {
			continue
		}
		lc.Expires++
		if listed[unit.String] {
			lc.Agree++
			continue
		}
		var why string
		switch {
		case abandoned.Bool:
			why = "shares an index with images strata keeps"
		case inIndex.Bool:
			why = "platform image of a tagged index"
		case desc.Int64 > 0:
			why = fmt.Sprintf("%d images are built on it", desc.Int64)
		case pulled.Valid:
			why = fmt.Sprintf("pulled %d days ago", pulled.Int64)
		default:
			why = "not abandoned under the staleness policy"
		}
		if len(lc.Conflicts) < maxConflicts {
			lc.Conflicts = append(lc.Conflicts, LifecycleConflict{Digest: digest.String, Reason: why})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(order)
	out := make([]Lifecycle, 0, len(order))
	for _, repo := range order {
		lc := byRepo[repo]
		conflicts := lc.Expires - lc.Agree
		if conflicts > 0 && !opts.IncludeConflicts {
			lc.Skipped = fmt.Sprintf("would expire %d image(s) strata keeps", conflicts)
		} else {
			lc.Policy = &LifecyclePolicy{Rules: []LifecycleRule{{
				RulePriority: 1,
				Description:  fmt.Sprintf("strata: expire untagged images pushed more than %d days ago", lc.Days),
				Selection:    LifecycleSelection{TagStatus: "untagged", CountType: "sinceImagePushed", CountUnit: "days", CountNumber: lc.Days},
				Action:       LifecycleAction{Type: "expire"},
			}}}
		}
		out = append(out, *lc)
	}
	return out, nil
}

// repoMatches reports whether a repo matches any glob, or there are none.
func repoMatches(repo string, globs []string) bool {
	if len(globs) == 0 {
		return true
	}
	for _, g := range globs {
		if ok, _ := path.Match(g, repo); ok {
			return true
		}
	}
	return false
}
