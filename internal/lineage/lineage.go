// Package lineage derives parent edges between images from their layer
// stacks, adds the parents images declare in annotations and labels, and
// materializes ancestry facts for fast queries.
//
// The parent of an image is the indexed stack that is the longest exact
// prefix of its own stack. Stacks are compared by chain ID, so the work is
// a handful of joins over image_prefixes rather than any layer comparison.
//
// When two or more stacks share a prefix that no indexed image ends at,
// that prefix is a common ancestor that was deleted or never pushed. It is
// kept as a phantom node keyed by its chain ID, so the tree stays intact.
package lineage

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/rossedman/strata/internal/model"
)

// Options controls which declared parents are read.
type Options struct {
	// Labels are label or annotation keys whose value names the image's
	// base, as a repo:tag or digest. The OCI base annotations are always
	// read.
	Labels []string
}

// Recompute rebuilds the edges, phantoms and lineage tables from the
// images in the index.
func Recompute(ctx context.Context, db *sql.DB, opts Options) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	stmts := []string{
		"BEGIN TRANSACTION",
		`DELETE FROM edges`,
		`DELETE FROM phantoms`,
		`DELETE FROM lineage`,
		// Every distinct stack, with its canonical image: the
		// earliest-created digest, so retags and rebuilds of the same
		// stack collapse into one node.
		`CREATE OR REPLACE TEMP TABLE stacks AS
			SELECT top_chain_id AS chain_id, any_value(layer_count) AS depth,
				first(digest ORDER BY created NULLS LAST, digest) AS canonical
			FROM images WHERE top_chain_id IS NOT NULL
			GROUP BY top_chain_id`,
		// Prefixes extended by two or more different next layers are
		// branch points; those no image ends at become phantoms.
		`CREATE OR REPLACE TEMP TABLE branches AS
			SELECT p.chain_id, any_value(p.depth) AS depth, count(DISTINCT c.chain_id) AS stacks
			FROM image_prefixes p
			JOIN image_prefixes c ON c.digest = p.digest AND c.depth = p.depth + 1
			GROUP BY p.chain_id
			HAVING count(DISTINCT c.chain_id) >= 2`,
		`INSERT INTO phantoms
			SELECT '` + model.PhantomPrefix + `' || b.chain_id, b.chain_id, b.depth, b.stacks
			FROM branches b WHERE b.chain_id NOT IN (SELECT chain_id FROM stacks)`,
		`CREATE OR REPLACE TEMP TABLE nodes AS
			SELECT chain_id, depth, canonical AS id FROM stacks
			UNION ALL SELECT chain_id, depth, id FROM phantoms`,
		// One image holding each node's prefix, to read its shorter
		// prefixes from.
		`CREATE OR REPLACE TEMP TABLE node_rep AS
			SELECT n.chain_id, n.depth, n.id, any_value(p.digest) AS rep
			FROM nodes n JOIN image_prefixes p ON p.chain_id = n.chain_id AND p.depth = n.depth
			GROUP BY n.chain_id, n.depth, n.id`,
		// A node's parent is the longest of its proper prefixes that is
		// itself a node.
		`CREATE OR REPLACE TEMP TABLE node_parent AS
			SELECT r.chain_id, max_by(n2.id, p.depth) AS parent, max(p.depth) AS parent_depth, any_value(r.depth) AS depth
			FROM node_rep r
			JOIN image_prefixes p ON p.digest = r.rep AND p.depth < r.depth
			JOIN nodes n2 ON n2.chain_id = p.chain_id
			GROUP BY r.chain_id`,
		`INSERT OR IGNORE INTO edges
			SELECT i.digest, np.parent, '` + model.SourceLayers + `', '` + model.ConfidenceExact + `',
				'shares ' || np.parent_depth || ' of ' || np.depth || ' layers'
			FROM images i JOIN node_parent np ON np.chain_id = i.top_chain_id
			WHERE np.parent <> i.digest`,
		`INSERT OR IGNORE INTO edges
			SELECT ph.id, np.parent, '` + model.SourceLayers + `', '` + model.ConfidenceExact + `',
				'shares ' || np.parent_depth || ' of ' || np.depth || ' layers'
			FROM phantoms ph JOIN node_parent np ON np.chain_id = ph.chain_id`,
	}
	stmts = append(stmts, declaredEdges(opts)...)
	stmts = append(stmts, materialize...)
	stmts = append(stmts, "COMMIT")
	for _, q := range stmts {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			return fmt.Errorf("recompute lineage: %w\n%s", err, q)
		}
	}
	return nil
}

// declaredEdges returns the statements that add parents named by the OCI
// base annotations and by the configured label keys. A declared parent
// that resolves to an index is matched to the child's platform.
func declaredEdges(opts Options) []string {
	stmts := []string{
		`CREATE OR REPLACE TEMP TABLE declared (child VARCHAR, ref VARCHAR, source VARCHAR, evidence VARCHAR)`,
		`INSERT INTO declared
			SELECT l.digest,
				coalesce(l.labels['org.opencontainers.image.base.digest']::VARCHAR,
				         l.labels['org.opencontainers.image.base.name']::VARCHAR),
				'` + model.SourceAnnotation + `',
				CASE WHEN l.labels['org.opencontainers.image.base.digest'] IS NOT NULL
				     THEN 'org.opencontainers.image.base.digest=' || l.labels['org.opencontainers.image.base.digest']::VARCHAR
				     ELSE 'org.opencontainers.image.base.name=' || l.labels['org.opencontainers.image.base.name']::VARCHAR END
			FROM labels l
			WHERE l.labels['org.opencontainers.image.base.digest'] IS NOT NULL
			   OR l.labels['org.opencontainers.image.base.name'] IS NOT NULL`,
	}
	for _, key := range opts.Labels {
		k := quote(key)
		stmts = append(stmts, `INSERT INTO declared
			SELECT l.digest, l.labels[`+k+`]::VARCHAR, '`+model.SourceLabel+`', `+k+` || '=' || l.labels[`+k+`]::VARCHAR
			FROM labels l WHERE l.labels[`+k+`] IS NOT NULL`)
	}
	return append(stmts,
		// A name with no host in an annotation means Docker Hub; try it
		// as written and with the docker.io/library prefix.
		`CREATE OR REPLACE TEMP TABLE declared_refs AS
			SELECT child, ref, source, evidence, ref AS candidate FROM declared
			UNION ALL SELECT child, ref, source, evidence, 'docker.io/' || ref FROM declared WHERE ref NOT LIKE '%/%/%' AND ref LIKE '%/%'
			UNION ALL SELECT child, ref, source, evidence, 'docker.io/library/' || ref FROM declared WHERE ref NOT LIKE '%/%'`,
		`INSERT OR IGNORE INTO edges
			SELECT d.child, coalesce(m.digest, i.digest, ti.digest, tm.digest) AS parent, d.source,
				'`+model.ConfidenceDeclared+`', d.evidence
			FROM declared_refs d
			JOIN images c ON c.digest = d.child
			LEFT JOIN images i ON i.digest = d.candidate OR i.repo || '@' || i.digest = d.candidate
			LEFT JOIN index_manifests m ON (m.index_digest = d.candidate OR d.candidate LIKE '%@' || m.index_digest)
				AND m.os = c.os AND m.arch = c.arch AND coalesce(m.variant, '') = coalesce(c.variant, '')
			LEFT JOIN tags t ON t.repo || ':' || t.tag = d.candidate
			LEFT JOIN images ti ON ti.digest = t.digest
			LEFT JOIN index_manifests tm ON tm.index_digest = t.digest
				AND tm.os = c.os AND tm.arch = c.arch AND coalesce(tm.variant, '') = coalesce(c.variant, '')
			WHERE coalesce(m.digest, i.digest, ti.digest, tm.digest) IS NOT NULL
			  AND coalesce(m.digest, i.digest, ti.digest, tm.digest) <> d.child`,
	)
}

// materialize turns edges into the lineage table: each node's preferred
// parent, its root, its generation and how many nodes descend from it.
var materialize = []string{
	`CREATE OR REPLACE TEMP TABLE preferred AS
		SELECT child, first(parent ORDER BY
				CASE source WHEN 'layers' THEN 1 WHEN 'attestation' THEN 2 WHEN 'annotation' THEN 3 ELSE 4 END, parent) AS parent,
			first(source ORDER BY
				CASE source WHEN 'layers' THEN 1 WHEN 'attestation' THEN 2 WHEN 'annotation' THEN 3 ELSE 4 END, parent) AS source
		FROM edges GROUP BY child`,
	// A conflict is a declared parent whose stack differs from the
	// inferred one. Comparing stacks, not digests, keeps a retag of the
	// same base from counting.
	`CREATE OR REPLACE TEMP TABLE conflicts AS
		SELECT DISTINCT l.child
		FROM edges l JOIN edges d ON d.child = l.child AND d.source <> 'layers'
		LEFT JOIN images lp ON lp.digest = l.parent
		LEFT JOIN images dp ON dp.digest = d.parent
		WHERE l.source = 'layers' AND coalesce(lp.top_chain_id, l.parent) <> coalesce(dp.top_chain_id, d.parent)`,
	`CREATE OR REPLACE TEMP TABLE all_nodes AS
		SELECT digest AS id FROM images UNION SELECT id FROM phantoms`,
	`CREATE OR REPLACE TEMP TABLE walk AS
		WITH RECURSIVE w(id, root, gen, path) AS (
			SELECT id, id, 0, []::VARCHAR[] FROM all_nodes WHERE id NOT IN (SELECT child FROM preferred)
			UNION ALL
			SELECT p.child, w.root, w.gen + 1, list_append(w.path, w.id)
			FROM preferred p JOIN w ON p.parent = w.id
			WHERE w.gen < 64
		)
		SELECT * FROM w`,
	`CREATE OR REPLACE TEMP TABLE desc_counts AS
		SELECT ancestor AS id, count(*) AS n FROM (SELECT unnest(path) AS ancestor FROM walk) GROUP BY ancestor`,
	`INSERT OR REPLACE INTO lineage
		SELECT n.id, p.parent, p.source, w.root, coalesce(w.gen, 0),
			(SELECT count(*) FROM preferred c WHERE c.parent = n.id),
			coalesce(dc.n, 0),
			n.id IN (SELECT child FROM conflicts)
		FROM all_nodes n
		LEFT JOIN preferred p ON p.child = n.id
		LEFT JOIN walk w ON w.id = n.id
		LEFT JOIN desc_counts dc ON dc.id = n.id`,
}

// quote returns s as a SQL string literal.
func quote(s string) string {
	out := []byte{'\''}
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'')
		}
		out = append(out, s[i])
	}
	return string(append(out, '\''))
}
