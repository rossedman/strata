// Package service answers strata's questions over an index: what an image
// is, where it came from, what is built on it, and whether it is stale.
// The CLI, the HTTP API and the MCP server all call it, so every interface
// gives the same answers.
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rossedman/strata/internal/crawl"
	"github.com/rossedman/strata/internal/lineage"
	"github.com/rossedman/strata/internal/stale"
	"github.com/rossedman/strata/internal/store"
)

// ErrNotFound is returned when a ref matches no indexed image.
var ErrNotFound = errors.New("not found in the index")

// Service runs queries against one index.
type Service struct {
	Store   *store.Store
	Policy  stale.Policy
	Lineage lineage.Options
	// Required are label keys every image should carry.
	Required []string
	// Synonyms map label keys that mean the same thing, such as owner to
	// team, for near-duplicate detection.
	Synonyms map[string]string
	// Platform is the default platform when a ref names an index.
	Platform string
	Now      func() time.Time
}

// New returns a service over an open store with the default policy.
func New(st *store.Store) *Service {
	return &Service{Store: st, Policy: stale.DefaultPolicy(), Platform: "linux/amd64", Now: time.Now}
}

// db returns the database handle.
func (s *Service) db() *sql.DB { return s.Store.DB() }

// IndexResult reports what an index run did.
type IndexResult struct {
	Crawl    crawl.Stats   `json:"crawl"`
	Images   int64         `json:"images"`
	Duration time.Duration `json:"duration"`
}

// Index crawls, then recomputes lineage, staleness and today's snapshot.
func (s *Service) Index(ctx context.Context, opts crawl.Options) (IndexResult, error) {
	start := s.Now()
	stats, err := crawl.Crawl(ctx, s.Store, opts)
	res := IndexResult{Crawl: stats}
	if err != nil {
		return res, err
	}
	if err := s.Refresh(ctx); err != nil {
		return res, err
	}
	if err := s.db().QueryRowContext(ctx, `SELECT count(*) FROM images`).Scan(&res.Images); err != nil {
		return res, err
	}
	res.Duration = s.Now().Sub(start)
	return res, nil
}

// Refresh recomputes the derived views: lineage, staleness and the daily
// snapshot.
func (s *Service) Refresh(ctx context.Context) error {
	if err := lineage.Recompute(ctx, s.db(), s.Lineage); err != nil {
		return err
	}
	if err := stale.Recompute(ctx, s.db(), s.Policy, s.Now()); err != nil {
		return err
	}
	return s.Snapshot(ctx)
}

// Resolve maps a ref to one platform manifest digest. It accepts a
// digest, a digest prefix of at least 12 hex characters, repo@digest,
// repo:tag, and repo:tag without the registry host when that is
// unambiguous. An index resolves to the platform given, or the service's
// default platform, or the first platform it holds.
func (s *Service) Resolve(ctx context.Context, ref, platform string) (string, error) {
	digests, err := s.resolveAll(ctx, ref)
	if err != nil {
		return "", err
	}
	asked := platform != ""
	if len(digests) == 1 && !asked {
		return digests[0], nil
	}
	if !asked {
		platform = s.Platform
	}
	rows, err := s.db().QueryContext(ctx, `
		SELECT digest, platform FROM image_view WHERE list_contains(?, digest) ORDER BY platform, digest`, digests)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var first, available []string
	for rows.Next() {
		var d, p string
		if err := rows.Scan(&d, &p); err != nil {
			return "", err
		}
		if p == platform {
			return d, nil
		}
		first = append(first, d)
		available = append(available, p)
	}
	if len(first) == 0 {
		return digests[0], nil
	}
	if asked {
		return "", input(fmt.Errorf("%s has no %s image (platforms: %s)", ref, platform, strings.Join(available, ", ")))
	}
	return first[0], nil
}

// resolveAll returns every digest a ref could mean.
func (s *Service) resolveAll(ctx context.Context, ref string) ([]string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, input(fmt.Errorf("empty ref"))
	}
	queries := []struct {
		q    string
		args []any
	}{
		{`SELECT DISTINCT digest FROM resolve_ref(?) ORDER BY digest`, []any{ref}},
		// A tag or digest without the registry host.
		{`SELECT DISTINCT coalesce(m.digest, t.digest) FROM tags t LEFT JOIN index_manifests m ON m.index_digest = t.digest
			WHERE (t.repo || ':' || t.tag) LIKE '%/' || ? OR (t.repo || '@' || t.digest) LIKE '%/' || ? ORDER BY 1`, []any{ref, ref}},
		{`SELECT DISTINCT digest FROM images WHERE (repo || '@' || digest) LIKE '%/' || ? ORDER BY 1`, []any{ref}},
	}
	if strings.HasPrefix(ref, "sha256:") && len(ref) >= 19 && len(ref) < 71 {
		queries = append(queries, struct {
			q    string
			args []any
		}{`SELECT DISTINCT digest FROM images WHERE starts_with(digest, ?) UNION
			SELECT DISTINCT m.digest FROM index_manifests m WHERE starts_with(m.index_digest, ?) ORDER BY 1`, []any{ref, ref}})
	}
	for _, q := range queries {
		ds, err := s.strings(ctx, q.q, q.args...)
		if err != nil {
			return nil, err
		}
		if len(ds) > 0 {
			if err := s.sameIndex(ctx, ref, ds); err != nil {
				return nil, err
			}
			return ds, nil
		}
	}
	return nil, fmt.Errorf("%s: %w", ref, ErrNotFound)
}

// sameIndex fails when a short ref matched images from more than one
// repo or index, which means it is ambiguous.
func (s *Service) sameIndex(ctx context.Context, ref string, digests []string) error {
	if len(digests) < 2 {
		return nil
	}
	groups, err := s.strings(ctx, `SELECT DISTINCT coalesce(index_digest, digest) FROM images WHERE list_contains(?, digest)`, digests)
	if err != nil {
		return err
	}
	if len(groups) > 1 {
		return input(fmt.Errorf("%s is ambiguous: it matches %d images; use the full ref or digest", ref, len(groups)))
	}
	return nil
}

// strings runs a query returning one string column.
func (s *Service) strings(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.db().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v sql.NullString
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		if v.Valid {
			out = append(out, v.String)
		}
	}
	return out, rows.Err()
}

// toStrings converts a scanned DuckDB list to strings.
func toStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		if x != nil {
			out = append(out, fmt.Sprint(x))
		}
	}
	sort.Strings(out)
	return out
}
