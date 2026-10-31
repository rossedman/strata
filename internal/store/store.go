// Package store keeps strata's index in a local DuckDB file.
//
// The index is a rebuildable cache of what the registry holds, with one
// exception: tag history, which the registry cannot give back. It can be
// exported to and imported from Parquet so it survives a rebuild.
package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"

	"github.com/rossedman/strata/internal/model"
)

// Store is an open index file.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens or creates the index at path, creating the schema and checking
// the format version. An empty path opens a private in-memory index.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := ""
	if path != "" {
		if dir := filepath.Dir(path); dir != "." {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return nil, fmt.Errorf("create index directory: %w", err)
			}
		}
		dsn = path
	}
	connector, err := duckdb.NewConnector(dsn+"?storage_compatibility_version=latest", nil)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	s := &Store{db: sql.OpenDB(connector), path: path}
	if err := s.migrate(ctx); err != nil {
		_ = s.db.Close()
		return nil, err
	}
	return s, nil
}

// migrate creates the schema and records the format version, refusing a
// file written by a newer strata.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schema[0]); err != nil {
		return fmt.Errorf("create meta table: %w", err)
	}
	if v, err := s.Meta(ctx, "format_version"); err != nil {
		return err
	} else if v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("index format version %q is not a number", v)
		}
		if n > FormatVersion {
			return fmt.Errorf("index format version %d is newer than this strata supports (%d); upgrade strata", n, FormatVersion)
		}
	}
	for _, stmt := range schema[1:] {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create schema: %w\n%s", err, stmt)
		}
	}
	var engine string
	if err := s.db.QueryRowContext(ctx, `SELECT library_version FROM pragma_version()`).Scan(&engine); err != nil {
		return fmt.Errorf("read engine version: %w", err)
	}
	if err := s.SetMeta(ctx, "format_version", strconv.Itoa(FormatVersion)); err != nil {
		return err
	}
	return s.SetMeta(ctx, "engine_version", engine)
}

// DB returns the underlying database handle for read queries.
func (s *Store) DB() *sql.DB { return s.db }

// Path returns the file the index lives in, or "" for an in-memory index.
func (s *Store) Path() string { return s.path }

// Close closes the index.
func (s *Store) Close() error { return s.db.Close() }

// Meta returns a value from the meta table, or "" if it is not set.
func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read meta %s: %w", key, err)
	}
	return v, nil
}

// SetMeta stores a value in the meta table.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	if _, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO meta VALUES (?, ?)`, key, value); err != nil {
		return fmt.Errorf("write meta %s: %w", key, err)
	}
	return nil
}

// Batch is a set of records written together in one transaction.
type Batch struct {
	Images     []*model.Image
	Index      []model.IndexEntry
	Tags       []model.Tag
	DeleteTags []model.Tag
	Events     []model.TagEvent
	Referrers  []model.Referrer
	// Pushes are push and pull times reported by the registry's API. They
	// change between crawls, so they replace what was stored.
	Pushes []model.Push
	// BaseRefs are the bases builds say images started from.
	BaseRefs []model.BaseRef
	// Run and Done checkpoint repos of a crawl run in the same
	// transaction as their data, so a resumed crawl never skips a repo
	// whose rows were lost.
	Run  int64
	Done []string
}

// Len returns the number of records in the batch.
func (b *Batch) Len() int {
	return len(b.Images) + len(b.Index) + len(b.Tags) + len(b.DeleteTags) + len(b.Events) + len(b.Referrers) + len(b.Pushes) + len(b.BaseRefs) + len(b.Done)
}

// Write stores a batch in one transaction, bulk-loading through DuckDB's
// appender. Images are content-addressed, so an image already in the index
// is left as it is.
func (s *Store) Write(ctx context.Context, b *Batch) error {
	if b.Len() == 0 {
		return nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN TRANSACTION"); err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	err = conn.Raw(func(dc any) error { return writeBatch(dc.(driver.Conn), b) })
	if err == nil {
		err = deleteTags(ctx, conn, b.DeleteTags)
	}
	if err != nil {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		return err
	}
	if _, err := conn.ExecContext(context.WithoutCancel(ctx), "COMMIT"); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// MergeRows makes the rows of table that match scope equal to rows, in
// one transaction. It bulk-loads rows into a staging table and writes only
// the differences: rows to remove, and rows to add or change, matched on
// the key columns. Rewriting rows that did not change is what makes a
// delete-and-reload slow on a table with a primary key. It returns how
// many rows it removed or wrote.
func (s *Store) MergeRows(ctx context.Context, table, scope string, scopeArgs []any, keys []string, rows [][]any) (int, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := Stage(ctx, conn, "merge_staged", table, func(a *duckdb.Appender) error {
		vals := make([]driver.Value, 0, 8)
		for _, r := range rows {
			vals = vals[:0]
			for _, v := range r {
				vals = append(vals, v)
			}
			if err := a.AppendRow(vals...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN TRANSACTION"); err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	n, err := mergeStaged(ctx, conn, table, scope, scopeArgs, keys)
	if err != nil {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		return 0, err
	}
	if _, err := conn.ExecContext(context.WithoutCancel(ctx), "COMMIT"); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return n, nil
}

// Stage creates a temporary table named temp with like's columns and
// bulk-loads it through the appender. Callers compare staged rows with
// stored ones and write the difference.
func Stage(ctx context.Context, conn *sql.Conn, temp, like string, rows func(a *duckdb.Appender) error) error {
	if err := CreateTempLike(ctx, conn, temp, like); err != nil {
		return err
	}
	return conn.Raw(func(dc any) error {
		return appendTo(dc.(driver.Conn), temp, "INSERT INTO "+temp+" SELECT * FROM appended_data", rows)
	})
}

// CreateTempLike creates (or replaces) an empty temporary table named
// temp with the columns and types of table like, declared explicitly.
//
// It does not use CREATE TABLE ... AS SELECT ... LIMIT 0. In DuckDB 2.0,
// a table created that way inside a transaction and then filled past one
// row group (122,880 rows) is invisible to semi-joins and EXCEPT until the
// transaction commits, so a diff against it silently comes out empty.
// TestCreateTempLikeLarge guards against this.
func CreateTempLike(ctx context.Context, conn *sql.Conn, temp, like string) error {
	rows, err := conn.QueryContext(ctx, `SELECT column_name, data_type FROM information_schema.columns
		WHERE table_name = ? AND table_schema = 'main' ORDER BY ordinal_position`, like)
	if err != nil {
		return fmt.Errorf("columns of %s: %w", like, err)
	}
	var cols []string
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			_ = rows.Close()
			return err
		}
		cols = append(cols, `"`+name+`" `+typ)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(cols) == 0 {
		return fmt.Errorf("columns of %s: no such table", like)
	}
	if _, err := conn.ExecContext(ctx, "CREATE OR REPLACE TEMP TABLE "+temp+" ("+strings.Join(cols, ", ")+")"); err != nil {
		return fmt.Errorf("create %s like %s: %w", temp, like, err)
	}
	return nil
}

// mergeStaged writes the differences between merge_staged and the rows of
// table in scope, inside the caller's transaction.
func mergeStaged(ctx context.Context, conn *sql.Conn, table, scope string, scopeArgs []any, keys []string) (int, error) {
	match := make([]string, len(keys))
	for i, k := range keys {
		match[i] = table + "." + k + " = g." + k
	}
	current := "SELECT * FROM " + table + " WHERE " + scope
	stmts := []struct {
		q    string
		args []any
	}{
		{"CREATE OR REPLACE TEMP TABLE merge_gone AS " + current + " EXCEPT SELECT * FROM merge_staged", scopeArgs},
		{"CREATE OR REPLACE TEMP TABLE merge_added AS SELECT * FROM merge_staged EXCEPT " + current, scopeArgs},
		{"DELETE FROM " + table + " USING merge_gone g WHERE " + strings.Join(match, " AND "), nil},
		{"INSERT INTO " + table + " SELECT * FROM merge_added", nil},
	}
	for _, st := range stmts {
		if _, err := conn.ExecContext(ctx, st.q, st.args...); err != nil {
			return 0, fmt.Errorf("merge %s: %w", table, err)
		}
	}
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM merge_gone) + (SELECT count(*) FROM merge_added)`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// deleteTags removes tags the registry no longer lists.
func deleteTags(ctx context.Context, conn *sql.Conn, tags []model.Tag) error {
	for _, t := range tags {
		if _, err := conn.ExecContext(ctx, `DELETE FROM tags WHERE repo = ? AND tag = ?`, t.Repo, t.Tag); err != nil {
			return fmt.Errorf("delete tag %s: %w", t.Ref(), err)
		}
	}
	return nil
}

// rowsFunc appends a table's rows to an appender.
type rowsFunc func(a *duckdb.Appender) error

// writeBatch appends every table's rows for a batch on one connection.
func writeBatch(conn driver.Conn, b *Batch) error {
	now := time.Now().UTC()
	// n is how many source records feed each table; tables with none are
	// skipped, since every appender costs a query even when empty.
	steps := []struct {
		table, query string
		rows         rowsFunc
		n            int
	}{
		{"images", "INSERT OR IGNORE INTO images SELECT * FROM appended_data", func(a *duckdb.Appender) error {
			for _, img := range b.Images {
				c := img.Config
				var created any
				if !img.Created.IsZero() {
					created = img.Created
				}
				if err := a.AppendRow(img.Digest, img.Repo, img.MediaType, nullable(img.IndexDigest),
					img.Platform.OS, img.Platform.Arch, img.Platform.Variant, created, img.Size,
					clamp32(len(img.Layers)), nullable(img.TopChainID()), img.ConfigDigest, img.External,
					c.User, nonNil(c.Entrypoint), nonNil(c.Cmd), nonNil(c.Ports), nonNil(c.EnvKeys),
					c.Workdir, c.StopSignal, c.Healthcheck, now); err != nil {
					return err
				}
			}
			return nil
		}, len(b.Images)},
		{"image_prefixes", "INSERT OR IGNORE INTO image_prefixes SELECT * FROM appended_data", func(a *duckdb.Appender) error {
			for _, img := range b.Images {
				for d, id := range img.ChainIDs {
					if err := a.AppendRow(img.Digest, clamp32(d+1), id); err != nil {
						return err
					}
				}
			}
			return nil
		}, len(b.Images)},
		{"layers", "INSERT OR IGNORE INTO layers SELECT DISTINCT * FROM appended_data", func(a *duckdb.Appender) error {
			for _, img := range b.Images {
				for _, l := range img.Layers {
					if err := a.AppendRow(l.DiffID, l.BlobDigest, l.Size, l.MediaType); err != nil {
						return err
					}
				}
			}
			return nil
		}, len(b.Images)},
		{"image_layers", "INSERT OR IGNORE INTO image_layers SELECT * FROM appended_data", func(a *duckdb.Appender) error {
			for _, img := range b.Images {
				for p, l := range img.Layers {
					if err := a.AppendRow(img.Digest, clamp32(p+1), l.DiffID, l.BlobDigest, l.Size, l.CreatedBy); err != nil {
						return err
					}
				}
			}
			return nil
		}, len(b.Images)},
		{"image_history", "INSERT OR IGNORE INTO image_history SELECT * FROM appended_data", func(a *duckdb.Appender) error {
			for _, img := range b.Images {
				for p, h := range img.History {
					if err := a.AppendRow(img.Digest, clamp32(p+1), h.CreatedBy, h.EmptyLayer); err != nil {
						return err
					}
				}
			}
			return nil
		}, len(b.Images)},
		{"env_values", "INSERT OR IGNORE INTO env_values SELECT * FROM appended_data", func(a *duckdb.Appender) error {
			for _, img := range b.Images {
				for k, v := range img.Config.EnvValues {
					if err := a.AppendRow(img.Digest, k, v); err != nil {
						return err
					}
				}
			}
			return nil
		}, len(b.Images)},
		{"index_manifests", "INSERT OR IGNORE INTO index_manifests SELECT * FROM appended_data", func(a *duckdb.Appender) error {
			for _, e := range b.Index {
				if err := a.AppendRow(e.IndexDigest, e.Digest, e.Platform.OS, e.Platform.Arch, e.Platform.Variant); err != nil {
					return err
				}
			}
			return nil
		}, len(b.Index)},
		{"tags", "INSERT OR REPLACE INTO tags SELECT * FROM appended_data", func(a *duckdb.Appender) error {
			for _, t := range b.Tags {
				if err := a.AppendRow(t.Repo, t.Tag, t.Digest, t.UpdatedAt); err != nil {
					return err
				}
			}
			return nil
		}, len(b.Tags)},
		{"tag_history", "INSERT OR IGNORE INTO tag_history SELECT * FROM appended_data", func(a *duckdb.Appender) error {
			for _, e := range b.Events {
				if err := a.AppendRow(e.Repo, e.Tag, e.SeenAt, nullable(e.Digest), e.Source); err != nil {
					return err
				}
			}
			return nil
		}, len(b.Events)},
		{"referrers", "INSERT OR IGNORE INTO referrers SELECT * FROM appended_data", func(a *duckdb.Appender) error {
			for _, r := range b.Referrers {
				if err := a.AppendRow(r.Subject, r.Digest, r.ArtifactType, r.Kind); err != nil {
					return err
				}
			}
			return nil
		}, len(b.Referrers)},
		{"pushes", `INSERT OR REPLACE INTO pushes
			SELECT col1, col2, min(col3), max(col4) FROM appended_data GROUP BY col1, col2`, func(a *duckdb.Appender) error {
			for _, p := range b.Pushes {
				if err := a.AppendRow(p.Repo, p.Digest, nullTime(p.PushedAt), nullTime(p.LastPulled)); err != nil {
					return err
				}
			}
			return nil
		}, len(b.Pushes)},
		// A registry's last-pull time feeds the same pull signal as imported
		// pull counts; the count is unknown.
		{"pulls", `INSERT OR REPLACE INTO pulls
			SELECT col1, col2, NULL, max(col4) FROM appended_data GROUP BY col1, col2`, func(a *duckdb.Appender) error {
			for _, p := range b.Pushes {
				if p.LastPulled.IsZero() {
					continue
				}
				t := p.LastPulled.UTC()
				if err := a.AppendRow(p.Digest, time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil, t); err != nil {
					return err
				}
			}
			return nil
		}, len(b.Pushes)},
		{"base_refs", "INSERT OR IGNORE INTO base_refs SELECT DISTINCT * FROM appended_data", func(a *duckdb.Appender) error {
			for _, r := range b.BaseRefs {
				if err := a.AppendRow(r.Digest, r.Ref, r.BaseDigest); err != nil {
					return err
				}
			}
			return nil
		}, len(b.BaseRefs)},
		{"crawl_repos", "INSERT OR REPLACE INTO crawl_repos SELECT * FROM appended_data", func(a *duckdb.Appender) error {
			for _, repo := range b.Done {
				if err := a.AppendRow(b.Run, repo, now); err != nil {
					return err
				}
			}
			return nil
		}, len(b.Done)},
	}
	for _, st := range steps {
		if st.n == 0 {
			continue
		}
		if err := appendTo(conn, st.table, st.query, st.rows); err != nil {
			return err
		}
	}
	if len(b.Images) == 0 {
		return nil
	}
	return appendLabels(conn, b.Images)
}

// appendTo runs one table appender with the given insert query.
func appendTo(conn driver.Conn, table, query string, rows rowsFunc) error {
	a, err := duckdb.NewTableAppender(conn, query, "", "", table, nil)
	if err != nil {
		return fmt.Errorf("appender for %s: %w", table, err)
	}
	if err := rows(a); err != nil {
		_ = a.Close()
		return fmt.Errorf("append %s: %w", table, err)
	}
	if err := a.Close(); err != nil {
		return fmt.Errorf("flush %s: %w", table, err)
	}
	return nil
}

// appendLabels stores each image's labels and annotations as one VARIANT,
// staged as JSON text because the appender has no VARIANT type.
func appendLabels(conn driver.Conn, images []*model.Image) error {
	vt, err := duckdb.NewTypeInfo(duckdb.TYPE_VARCHAR)
	if err != nil {
		return err
	}
	a, err := duckdb.NewQueryAppender(conn,
		"INSERT OR IGNORE INTO labels SELECT col1, col2::JSON::VARIANT FROM appended_data",
		"", []duckdb.TypeInfo{vt, vt}, nil)
	if err != nil {
		return fmt.Errorf("appender for labels: %w", err)
	}
	for _, img := range images {
		js, err := json.Marshal(img.Metadata())
		if err != nil {
			_ = a.Close()
			return err
		}
		if err := a.AppendRow(img.Digest, string(js)); err != nil {
			_ = a.Close()
			return fmt.Errorf("append labels: %w", err)
		}
	}
	if err := a.Close(); err != nil {
		return fmt.Errorf("flush labels: %w", err)
	}
	return nil
}

// clamp32 converts an int to int32, saturating at the type's bounds.
func clamp32(n int) int32 {
	n = max(math.MinInt32, min(math.MaxInt32, n))
	return int32(n) //nolint:gosec // clamped to the int32 range above
}

// nullable maps "" to SQL NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullTime returns nil for the zero time, so it is stored as NULL.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// nonNil returns an empty slice for nil, so list columns hold [] not NULL.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// KnownDigests returns the manifest and index digests already indexed for
// a repo, so a re-crawl only fetches what is new.
func (s *Store) KnownDigests(ctx context.Context, repo string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT digest FROM images WHERE repo = ?
		UNION SELECT m.index_digest FROM index_manifests m JOIN images i ON i.digest = m.digest WHERE i.repo = ?`, repo, repo)
	if err != nil {
		return nil, fmt.Errorf("known digests: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out[d] = true
	}
	return out, rows.Err()
}

// KnownImage reports whether a manifest digest is indexed in any repo.
func (s *Store) KnownImage(ctx context.Context, digest string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM (SELECT digest FROM images WHERE digest = ? UNION ALL
		SELECT index_digest FROM index_manifests WHERE index_digest = ?)`, digest, digest).Scan(&n)
	return n > 0, err
}

// MissingBases returns the bases builds named, by digest, that are not
// indexed and were not tried since retryAfter.
func (s *Store) MissingBases(ctx context.Context, retryAfter time.Time) ([]model.BaseRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT any_value(b.ref ORDER BY b.ref), b.base_digest FROM base_refs b
		WHERE b.base_digest <> ''
		  AND b.base_digest NOT IN (SELECT digest FROM images)
		  AND b.base_digest NOT IN (SELECT index_digest FROM index_manifests)
		  AND b.base_digest NOT IN (SELECT base_digest FROM base_fetches WHERE tried_at > ?)
		GROUP BY b.base_digest ORDER BY b.base_digest`, retryAfter)
	if err != nil {
		return nil, fmt.Errorf("read missing bases: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []model.BaseRef
	for rows.Next() {
		var r model.BaseRef
		if err := rows.Scan(&r.Ref, &r.BaseDigest); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordBaseFetch records an attempt to fetch a base by digest; errText
// is empty when it succeeded.
func (s *Store) RecordBaseFetch(ctx context.Context, r model.BaseRef, errText string, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO base_fetches VALUES (?, ?, ?, ?)`, r.BaseDigest, r.Ref, nullable(errText), at); err != nil {
		return fmt.Errorf("record base fetch: %w", err)
	}
	return nil
}

// Tags returns where each tag of a repo pointed at the last crawl.
func (s *Store) Tags(ctx context.Context, repo string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tag, digest FROM tags WHERE repo = ?`, repo)
	if err != nil {
		return nil, fmt.Errorf("read tags: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var t, d string
		if err := rows.Scan(&t, &d); err != nil {
			return nil, err
		}
		out[t] = d
	}
	return out, rows.Err()
}

// StartCrawl returns the crawl run to work on for a registry: the last one
// if it never finished, so a crash resumes, or a new one. It also returns
// the repos the run already finished.
func (s *Store) StartCrawl(ctx context.Context, registry string) (int64, map[string]bool, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM crawl_runs WHERE registry = ? AND finished_at IS NULL
		ORDER BY started_at DESC LIMIT 1`, registry).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(id), 0) + 1 FROM crawl_runs`).Scan(&id); err != nil {
			return 0, nil, fmt.Errorf("next crawl id: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO crawl_runs VALUES (?, ?, now(), NULL)`, id, registry); err != nil {
			return 0, nil, fmt.Errorf("start crawl: %w", err)
		}
		return id, map[string]bool{}, nil
	case err != nil:
		return 0, nil, fmt.Errorf("find crawl: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT repo FROM crawl_repos WHERE run_id = ?`, id)
	if err != nil {
		return 0, nil, fmt.Errorf("read checkpoints: %w", err)
	}
	defer func() { _ = rows.Close() }()
	done := map[string]bool{}
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return 0, nil, err
		}
		done[r] = true
	}
	return id, done, rows.Err()
}

// CheckpointRepo records that a crawl run finished a repo.
func (s *Store) CheckpointRepo(ctx context.Context, run int64, repo string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO crawl_repos VALUES (?, ?, now())`, run, repo)
	if err != nil {
		return fmt.Errorf("checkpoint %s: %w", repo, err)
	}
	return nil
}

// FinishCrawl marks a crawl run complete.
func (s *Store) FinishCrawl(ctx context.Context, run int64) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE crawl_runs SET finished_at = now() WHERE id = ?`, run); err != nil {
		return fmt.Errorf("finish crawl: %w", err)
	}
	return nil
}

// ExportHistory writes tag history to a Parquet file.
func (s *Store) ExportHistory(ctx context.Context, path string) (int64, error) {
	// COPY takes no parameters; the path is quoted as a literal.
	q := fmt.Sprintf(`COPY (SELECT * FROM tag_history ORDER BY repo, tag, seen_at) TO %s (FORMAT parquet)`, quoteLiteral(path)) //nolint:gosec // path is escaped by quoteLiteral
	if _, err := s.db.ExecContext(ctx, q); err != nil {
		return 0, fmt.Errorf("export history: %w", err)
	}
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM tag_history`).Scan(&n)
	return n, err
}

// ImportHistory merges tag history from a Parquet file written by
// ExportHistory, keeping rows already present.
func (s *Store) ImportHistory(ctx context.Context, path string) (int64, error) {
	var before, after int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM tag_history`).Scan(&before); err != nil {
		return 0, err
	}
	q := "INSERT OR IGNORE INTO tag_history SELECT repo, tag, seen_at, digest, source FROM read_parquet(" + quoteLiteral(path) + ")" //nolint:gosec // path is escaped by quoteLiteral
	if _, err := s.db.ExecContext(ctx, q); err != nil {
		return 0, fmt.Errorf("import history: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM tag_history`).Scan(&after); err != nil {
		return 0, err
	}
	return after - before, nil
}

// quoteLiteral quotes s as a SQL string literal, for statements such as
// COPY that do not accept parameters.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
