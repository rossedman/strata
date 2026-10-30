package service

import (
	"context"
	"fmt"
	"sort"
)

// LayerStat is one layer blob and how it is used.
type LayerStat struct {
	DiffID    string `json:"diff_id"`
	Blob      string `json:"blob_digest"`
	Size      int64  `json:"size"`
	Images    int64  `json:"images"`
	Repos     int64  `json:"repos"`
	CreatedBy string `json:"created_by"`
	Example   string `json:"example_repo"`
}

// TopLayers lists layers by how many images share them, or by size.
func (s *Service) TopLayers(ctx context.Context, by string, limit int) ([]LayerStat, error) {
	order := "images DESC, size DESC"
	switch by {
	case "", "shares":
	case "size":
		order = "size DESC, images DESC"
	default:
		return nil, fmt.Errorf("--by must be shares or size, not %q", by)
	}
	return s.layerStats(ctx, "", order, limit)
}

// Bloat lists large layers that only one image, or only one repo, uses.
func (s *Service) Bloat(ctx context.Context, perRepo bool, minSize int64, limit int) ([]LayerStat, error) {
	having := "count(DISTINCT il.digest) = 1"
	if perRepo {
		having = "count(DISTINCT i.repo) = 1"
	}
	return s.layerStats(ctx, fmt.Sprintf("HAVING %s AND any_value(il.size) >= %d", having, minSize), "size DESC", limit)
}

// layerStats aggregates image_layers per blob.
func (s *Service) layerStats(ctx context.Context, having, order string, limit int) ([]LayerStat, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT il.diff_id, il.blob_digest, any_value(il.size) AS size, count(DISTINCT il.digest) AS images,
			count(DISTINCT i.repo) AS repos, coalesce(any_value(il.created_by), ''), min(i.repo)
		FROM image_layers il JOIN images i ON i.digest = il.digest
		GROUP BY il.diff_id, il.blob_digest ` + having + ` ORDER BY ` + order + `, il.blob_digest LIMIT ?`
	rows, err := s.db().QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("layer stats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []LayerStat{}
	for rows.Next() {
		var l LayerStat
		if err := rows.Scan(&l.DiffID, &l.Blob, &l.Size, &l.Images, &l.Repos, &l.CreatedBy, &l.Example); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// BaseCluster is a group of bases whose stacks match except for their
// last one or two layers.
type BaseCluster struct {
	SharedLayers int      `json:"shared_layers"`
	Bases        []string `json:"bases"`
	Descendants  int64    `json:"descendants"`
	// SavedBytes estimates the deduplicated bytes saved by standardizing
	// on one base: the size of the differing top layers of all but the
	// largest base.
	SavedBytes int64 `json:"saved_bytes"`
}

// SimilarBases finds near-duplicate bases: images with descendants whose
// stacks share every layer but the last one or two.
func (s *Service) SimilarBases(ctx context.Context) ([]BaseCluster, error) {
	rows, err := s.db().QueryContext(ctx, `
		WITH bases AS (
			SELECT i.digest, i.layer_count, l.descendants FROM images i JOIN lineage l USING (digest)
			WHERE l.descendants > 0 AND i.layer_count >= 2),
		keyed AS (
			SELECT b.digest, b.layer_count, b.descendants, k.drop, p.chain_id AS prefix
			FROM bases b, (VALUES (1), (2)) k(drop)
			JOIN image_prefixes p ON p.digest = b.digest AND p.depth = b.layer_count - k.drop
			WHERE b.layer_count > k.drop),
		top_bytes AS (
			SELECT k.digest, k.drop, sum(il.size) AS bytes FROM keyed k
			JOIN image_layers il ON il.digest = k.digest AND il.position > k.layer_count - k.drop GROUP BY 1, 2)
		SELECT k.prefix, any_value(k.layer_count - k.drop), list(DISTINCT k.digest ORDER BY k.digest), sum(k.descendants),
			sum(t.bytes) - max(t.bytes)
		FROM keyed k JOIN top_bytes t USING (digest, drop)
		GROUP BY k.prefix, k.drop HAVING count(DISTINCT k.digest) > 1
		ORDER BY 5 DESC NULLS LAST, 1`)
	if err != nil {
		return nil, fmt.Errorf("similar bases: %w", err)
	}
	var clusters []BaseCluster
	var all []string
	for rows.Next() {
		var prefix string
		var c BaseCluster
		var bases any
		if err := rows.Scan(&prefix, &c.SharedLayers, &bases, &c.Descendants, &c.SavedBytes); err != nil {
			_ = rows.Close()
			return nil, err
		}
		c.Bases = unsorted(bases)
		all = append(all, c.Bases...)
		clusters = append(clusters, c)
	}
	_ = rows.Close()
	// A pair that differs in one layer also differs in two; keep the
	// tighter cluster only.
	clusters = dedupeClusters(clusters)
	nodes, err := s.nodes(ctx, all)
	if err != nil {
		return nil, err
	}
	for i := range clusters {
		for j, d := range clusters[i].Bases {
			clusters[i].Bases[j] = nodes[d].Label
		}
		sort.Strings(clusters[i].Bases)
	}
	return clusters, nil
}

// dedupeClusters drops clusters whose base set equals one with more
// shared layers.
func dedupeClusters(cs []BaseCluster) []BaseCluster {
	best := map[string]BaseCluster{}
	var order []string
	for _, c := range cs {
		k := fmt.Sprint(c.Bases)
		if old, ok := best[k]; !ok {
			best[k] = c
			order = append(order, k)
		} else if c.SharedLayers > old.SharedLayers {
			best[k] = c
		}
	}
	out := make([]BaseCluster, 0, len(order))
	for _, k := range order {
		out = append(out, best[k])
	}
	return out
}
