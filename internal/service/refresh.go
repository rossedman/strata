package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rossedman/strata/internal/lineage"
	"github.com/rossedman/strata/internal/stale"
)

// RefreshStats reports what a refresh did, so its cost can be traced to
// what changed.
type RefreshStats struct {
	// Full is true when every derived table was rebuilt: the first
	// refresh, or one after the image_facts layout changed.
	Full bool `json:"full"`
	// LineageSkipped is true when no input to lineage changed.
	LineageSkipped bool `json:"lineage_skipped"`
	// LineageChanged, VerdictsChanged and FactsRebuilt count the rows each
	// step rewrote.
	LineageChanged  int `json:"lineage_changed"`
	VerdictsChanged int `json:"verdicts_changed"`
	FactsRebuilt    int `json:"facts_rebuilt"`
	// SnapshotRows counts today's snapshot rows added, changed or removed;
	// SnapshotSkipped is true when no image row changed since today's.
	SnapshotRows    int           `json:"snapshot_rows"`
	SnapshotSkipped bool          `json:"snapshot_skipped"`
	Duration        time.Duration `json:"duration"`
}

// factsLiveView serves image_facts with ages computed when queried, so a
// row that nothing else changed does not need rewriting as days pass.
const factsLiveView = `CREATE OR REPLACE VIEW image_facts_live AS
	SELECT * REPLACE (
		CASE WHEN created IS NULL OR year(created) < 1980 THEN NULL ELSE date_diff('day', created, now()) END AS age_days,
		date_diff('day', pushed, now()) AS pushed_age_days)
	FROM image_facts`

// Shadow tables hold what tags, referrers and push times looked like at
// the last refresh. Comparing against them finds the images whose cached
// refs, referrer flags or push times are out of date.
var shadows = map[string]string{
	"refresh_tags":      "SELECT repo, tag, digest FROM tags",
	"refresh_referrers": "SELECT subject, digest, kind FROM referrers",
	"refresh_pushes":    "SELECT repo, digest, pushed_at FROM pushes",
}

// refresh does the work of Refresh; the caller holds refreshMu.
//
// Each step does only what the changes since the last refresh call for:
//
//   - Lineage is skipped when no image, tag, index entry or lineage
//     setting changed, and otherwise writes only the rows that differ.
//   - Staleness depends on the time, so every image is evaluated, but only
//     changed verdicts are written.
//   - Subtree stats are recomputed only when lineage or a verdict changed.
//   - image_facts is rebuilt only for images whose own row, lineage,
//     verdict, tags, referrers or push times changed, or whose parent's or
//     root's tags did.
//   - Today's snapshot is skipped when no image row changed, and
//     otherwise writes only the metrics that moved.
func (s *Service) refresh(ctx context.Context) error {
	start := time.Now()
	stats := RefreshStats{}
	full, err := s.needsFull(ctx)
	if err != nil {
		return err
	}
	stats.Full = full

	var linChanged []string
	fp, err := s.lineageFingerprint(ctx)
	if err != nil {
		return err
	}
	prev, err := s.Store.Meta(ctx, "lineage_fingerprint")
	if err != nil {
		return err
	}
	if full || fp != prev {
		if linChanged, err = lineage.Recompute(ctx, s.db(), s.Lineage); err != nil {
			return err
		}
		if err := s.Store.SetMeta(ctx, "lineage_fingerprint", fp); err != nil {
			return err
		}
	} else {
		stats.LineageSkipped = true
	}
	stats.LineageChanged = len(linChanged)

	staleChanged, err := stale.Recompute(ctx, s.db(), s.Policy, s.Now())
	if err != nil {
		return err
	}
	stats.VerdictsChanged = len(staleChanged)

	if full || len(linChanged) > 0 || len(staleChanged) > 0 {
		if err := s.subtreeStats(ctx); err != nil {
			return err
		}
	}
	if full {
		if err := s.materializeFacts(ctx); err != nil {
			return err
		}
		stats.FactsRebuilt = -1
	} else if stats.FactsRebuilt, err = s.updateFacts(ctx, append(linChanged, staleChanged...)); err != nil {
		return err
	}
	if err := s.saveShadows(ctx); err != nil {
		return err
	}
	// Snapshot metrics come from image rows, layers and labels alone, so
	// when no image row changed they match what was written earlier today,
	// provided today's rows are still there.
	key := s.Now().UTC().Format(time.DateOnly) + "|" + strings.Join(s.Required, ",")
	last, err := s.Store.Meta(ctx, "snapshot_key")
	if err != nil {
		return err
	}
	var today bool
	if err := s.db().QueryRowContext(ctx, `SELECT count(*) > 0 FROM snapshots WHERE day = ?`, strings.Split(key, "|")[0]).Scan(&today); err != nil {
		return err
	}
	if full || stats.FactsRebuilt != 0 || key != last || !today {
		if stats.SnapshotRows, err = s.snapshot(ctx); err != nil {
			return err
		}
		if err := s.Store.SetMeta(ctx, "snapshot_key", key); err != nil {
			return err
		}
	} else {
		stats.SnapshotSkipped = true
	}
	stats.Duration = time.Since(start)
	s.update(func(st *IndexStatus) { st.LastRefresh = &stats })
	s.logger().Debug("refreshed", "full", stats.Full, "lineage_skipped", stats.LineageSkipped,
		"lineage_changed", stats.LineageChanged, "verdicts_changed", stats.VerdictsChanged,
		"facts_rebuilt", stats.FactsRebuilt, "snapshot_rows", stats.SnapshotRows, "snapshot_skipped", stats.SnapshotSkipped, "took", stats.Duration)
	return nil
}

// needsFull reports whether image_facts must be rebuilt in full: it does
// not exist yet, its columns no longer match image_view, or the shadow
// tables an incremental update compares against are missing.
func (s *Service) needsFull(ctx context.Context) (bool, error) {
	if !s.facts.Load() {
		return true, nil
	}
	cols := func(table string) ([]string, error) {
		return s.strings(ctx, `SELECT column_name FROM information_schema.columns WHERE table_name = ? ORDER BY ordinal_position`, table)
	}
	view, err := cols("image_view")
	if err != nil {
		return false, err
	}
	facts, err := cols("image_facts")
	if err != nil {
		return false, err
	}
	if !slices.Equal(view, facts) {
		return true, nil
	}
	var n int
	if err := s.db().QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_name IN ('refresh_tags', 'refresh_referrers', 'refresh_pushes')`).Scan(&n); err != nil {
		return false, err
	}
	return n < len(shadows), nil
}

// lineageFingerprint summarizes every input to lineage: the images (with
// their layers and labels, which never change once indexed), the tags and
// index entries declared parents resolve through, and the label keys
// read as declared parents.
func (s *Service) lineageFingerprint(ctx context.Context) (string, error) {
	var fp string
	err := s.db().QueryRowContext(ctx, `SELECT
		(SELECT count(*) || ':' || coalesce(max(indexed_at)::VARCHAR, '') FROM images) || '|' ||
		(SELECT count(*) || ':' || coalesce(bit_xor(hash(repo, tag, digest)), 0) FROM tags) || '|' ||
		(SELECT count(*) || ':' || coalesce(bit_xor(hash(index_digest, digest)), 0) FROM index_manifests) || '|' ||
		(SELECT count(*) || ':' || coalesce(bit_xor(hash(digest, ref, base_digest)), 0) FROM base_refs)`).Scan(&fp)
	if err != nil {
		return "", fmt.Errorf("lineage fingerprint: %w", err)
	}
	return fp + "|" + strings.Join(s.Lineage.Labels, ","), nil
}

// materializeFacts writes image_view into the image_facts table, so the
// UI, queries and reports scan one columnar table instead of re-joining
// refs, tags, lineage, staleness and referrers for every image.
func (s *Service) materializeFacts(ctx context.Context) error {
	for _, q := range []string{
		`CREATE OR REPLACE TABLE image_facts AS SELECT * FROM image_view`,
		`CREATE INDEX IF NOT EXISTS image_facts_digest ON image_facts (digest)`,
		factsLiveView,
	} {
		if _, err := s.db().ExecContext(ctx, q); err != nil {
			return fmt.Errorf("materialize image facts: %w", err)
		}
	}
	s.facts.Store(true)
	return nil
}

// updateFacts rebuilds the image_facts rows that changed since the last
// refresh and returns how many it rebuilt. changed are the digests whose
// lineage or staleness rows were rewritten.
func (s *Service) updateFacts(ctx context.Context, changed []string) (int, error) {
	tx, err := s.db().BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	// A tag, referrer or push time changing on a digest changes the row of
	// that image, or of every platform image when the digest is an index.
	// Tags also show in the rows of images whose parent or root they name.
	stmts := []string{
		`CREATE OR REPLACE TEMP TABLE touched AS
			SELECT digest AS d FROM (SELECT repo, tag, digest FROM tags EXCEPT SELECT * FROM refresh_tags)
			UNION SELECT digest FROM (SELECT * FROM refresh_tags EXCEPT SELECT repo, tag, digest FROM tags)
			UNION SELECT subject FROM (SELECT subject, digest, kind FROM referrers EXCEPT SELECT * FROM refresh_referrers)
			UNION SELECT subject FROM (SELECT * FROM refresh_referrers EXCEPT SELECT subject, digest, kind FROM referrers)
			UNION SELECT digest FROM (SELECT repo, digest, pushed_at FROM pushes EXCEPT SELECT * FROM refresh_pushes)
			UNION SELECT digest FROM (SELECT * FROM refresh_pushes EXCEPT SELECT repo, digest, pushed_at FROM pushes)`,
		`CREATE OR REPLACE TEMP TABLE touched_images AS
			SELECT d FROM touched UNION SELECT m.digest FROM index_manifests m JOIN touched t ON t.d = m.index_digest`,
		`CREATE OR REPLACE TEMP TABLE facts_dirty AS
			SELECT unnest(?::VARCHAR[]) AS digest
			UNION SELECT d FROM touched_images
			UNION SELECT l.digest FROM lineage l JOIN touched_images t ON t.d = l.parent OR t.d = l.root
			UNION SELECT i.digest FROM images i ANTI JOIN image_facts f ON f.digest = i.digest`,
		`DELETE FROM image_facts WHERE digest IN (SELECT digest FROM facts_dirty)`,
		`INSERT INTO image_facts SELECT * FROM image_view WHERE digest IN (SELECT digest FROM facts_dirty)`,
	}
	for i, q := range stmts {
		var args []any
		if i == 2 {
			args = []any{changed}
		}
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return 0, fmt.Errorf("update image facts: %w", err)
		}
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM facts_dirty f SEMI JOIN images i ON i.digest = f.digest`).Scan(&n); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// saveShadows records tags, referrers and push times as they are now, for
// the next refresh to compare against.
func (s *Service) saveShadows(ctx context.Context) error {
	tx, err := s.db().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for table, q := range shadows {
		if _, err := tx.ExecContext(ctx, `CREATE OR REPLACE TABLE `+table+` AS `+q); err != nil {
			return fmt.Errorf("save %s: %w", table, err)
		}
	}
	return tx.Commit()
}
