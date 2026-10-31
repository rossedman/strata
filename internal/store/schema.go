package store

// FormatVersion is the version of strata's table layout. It is recorded in
// the file's meta table, and Open refuses a file written by a newer strata.
const FormatVersion = 1

// schema creates every table, index, macro and view strata uses. Each
// statement is idempotent, so it runs on every Open.
var schema = []string{
	`CREATE TABLE IF NOT EXISTS meta (
		key   VARCHAR PRIMARY KEY,
		value VARCHAR
	)`,
	`CREATE TABLE IF NOT EXISTS images (
		digest             VARCHAR PRIMARY KEY,
		repo               VARCHAR NOT NULL,
		media_type         VARCHAR,
		index_digest       VARCHAR,
		os                 VARCHAR,
		arch               VARCHAR,
		variant            VARCHAR,
		created            TIMESTAMPTZ,
		size               BIGINT,
		layer_count        INTEGER,
		top_chain_id       VARCHAR,
		config_digest      VARCHAR,
		external           BOOLEAN,
		config_user        VARCHAR,
		config_entrypoint  VARCHAR[],
		config_cmd         VARCHAR[],
		config_ports       VARCHAR[],
		config_env_keys    VARCHAR[],
		config_workdir     VARCHAR,
		config_stop_signal VARCHAR,
		config_healthcheck BOOLEAN,
		indexed_at         TIMESTAMPTZ
	)`,
	`CREATE INDEX IF NOT EXISTS images_top_chain ON images (top_chain_id)`,
	`CREATE TABLE IF NOT EXISTS image_prefixes (
		digest   VARCHAR,
		depth    INTEGER,
		chain_id VARCHAR,
		PRIMARY KEY (digest, depth)
	)`,
	`CREATE INDEX IF NOT EXISTS image_prefixes_chain ON image_prefixes (chain_id)`,
	`CREATE TABLE IF NOT EXISTS labels (
		digest VARCHAR PRIMARY KEY,
		labels VARIANT
	)`,
	`CREATE TABLE IF NOT EXISTS env_values (
		digest VARCHAR,
		key    VARCHAR,
		value  VARCHAR,
		PRIMARY KEY (digest, key)
	)`,
	`CREATE TABLE IF NOT EXISTS index_manifests (
		index_digest VARCHAR,
		digest       VARCHAR,
		os           VARCHAR,
		arch         VARCHAR,
		variant      VARCHAR,
		PRIMARY KEY (index_digest, digest)
	)`,
	`CREATE TABLE IF NOT EXISTS tags (
		repo       VARCHAR,
		tag        VARCHAR,
		digest     VARCHAR,
		updated_at TIMESTAMPTZ,
		PRIMARY KEY (repo, tag)
	)`,
	`CREATE INDEX IF NOT EXISTS tags_digest ON tags (digest)`,
	`CREATE TABLE IF NOT EXISTS tag_history (
		repo    VARCHAR,
		tag     VARCHAR,
		seen_at TIMESTAMPTZ,
		digest  VARCHAR,
		source  VARCHAR,
		PRIMARY KEY (repo, tag, seen_at)
	)`,
	`CREATE TABLE IF NOT EXISTS layers (
		diff_id     VARCHAR,
		blob_digest VARCHAR,
		size        BIGINT,
		media_type  VARCHAR,
		PRIMARY KEY (diff_id, blob_digest)
	)`,
	`CREATE TABLE IF NOT EXISTS image_layers (
		digest      VARCHAR,
		position    INTEGER,
		diff_id     VARCHAR,
		blob_digest VARCHAR,
		size        BIGINT,
		created_by  VARCHAR,
		PRIMARY KEY (digest, position)
	)`,
	`CREATE TABLE IF NOT EXISTS image_history (
		digest      VARCHAR,
		position    INTEGER,
		created_by  VARCHAR,
		empty_layer BOOLEAN,
		PRIMARY KEY (digest, position)
	)`,
	`CREATE TABLE IF NOT EXISTS referrers (
		subject       VARCHAR,
		digest        VARCHAR,
		artifact_type VARCHAR,
		kind          VARCHAR,
		PRIMARY KEY (subject, digest)
	)`,
	`CREATE TABLE IF NOT EXISTS pulls (
		digest      VARCHAR,
		day         DATE,
		count       BIGINT,
		last_pulled TIMESTAMPTZ,
		PRIMARY KEY (digest, day)
	)`,
	`CREATE TABLE IF NOT EXISTS pushes (
		repo        VARCHAR,
		digest      VARCHAR,
		pushed_at   TIMESTAMPTZ,
		last_pulled TIMESTAMPTZ,
		PRIMARY KEY (repo, digest)
	)`,
	// scans holds the registry's vulnerability scan of each manifest.
	// findings is whether the vulnerabilities rows for the scan were
	// fetched, at fetched_at; fixable counts those with a fixed version.
	`CREATE TABLE IF NOT EXISTS scans (
		digest       VARCHAR PRIMARY KEY,
		source       VARCHAR,
		status       VARCHAR,
		completed_at TIMESTAMPTZ,
		critical     INTEGER,
		high         INTEGER,
		medium       INTEGER,
		low          INTEGER,
		other        INTEGER,
		findings     BOOLEAN,
		fixable      INTEGER,
		fetched_at   TIMESTAMPTZ
	)`,
	`CREATE TABLE IF NOT EXISTS vulnerabilities (
		digest   VARCHAR,
		id       VARCHAR,
		package  VARCHAR,
		version  VARCHAR,
		severity VARCHAR,
		fixed_in VARCHAR,
		uri      VARCHAR,
		PRIMARY KEY (digest, id, package)
	)`,
	`CREATE INDEX IF NOT EXISTS vulnerabilities_id ON vulnerabilities (id)`,
	`CREATE TABLE IF NOT EXISTS edges (
		child      VARCHAR,
		parent     VARCHAR,
		source     VARCHAR,
		confidence VARCHAR,
		evidence   VARCHAR,
		PRIMARY KEY (child, parent, source)
	)`,
	`CREATE INDEX IF NOT EXISTS edges_parent ON edges (parent)`,
	`CREATE TABLE IF NOT EXISTS phantoms (
		id       VARCHAR PRIMARY KEY,
		chain_id VARCHAR,
		depth    INTEGER,
		stacks   INTEGER
	)`,
	`CREATE TABLE IF NOT EXISTS lineage (
		digest        VARCHAR PRIMARY KEY,
		parent        VARCHAR,
		parent_source VARCHAR,
		root          VARCHAR,
		generation    INTEGER,
		children      INTEGER,
		descendants   INTEGER,
		conflict      BOOLEAN
	)`,
	`CREATE INDEX IF NOT EXISTS lineage_parent ON lineage (parent)`,
	// subtree_stats holds, for every lineage node, the images and verdict
	// counts of its whole subtree, itself included, so the web UI can show
	// any outline row with one lookup.
	`CREATE TABLE IF NOT EXISTS subtree_stats (
		id        VARCHAR PRIMARY KEY,
		images    BIGINT,
		fresh     BIGINT,
		drifting  BIGINT,
		stale     BIGINT,
		abandoned BIGINT,
		newest    TIMESTAMPTZ
	)`,
	`CREATE TABLE IF NOT EXISTS staleness (
		digest                 VARCHAR PRIMARY KEY,
		verdict                VARCHAR,
		reasons                VARIANT,
		base_drift_generations INTEGER,
		base_drift_days        INTEGER,
		base_tag               VARCHAR,
		superseded_days        INTEGER,
		untagged               BOOLEAN,
		age_days               INTEGER,
		last_pulled_days       INTEGER,
		eol_base               BOOLEAN,
		label_policy           BOOLEAN,
		inferred               BOOLEAN,
		computed_at            TIMESTAMPTZ
	)`,
	`CREATE TABLE IF NOT EXISTS snapshots (
		day         DATE,
		group_by    VARCHAR,
		group_value VARCHAR,
		metric      VARCHAR,
		value       DOUBLE,
		PRIMARY KEY (day, group_by, group_value, metric)
	)`,
	`CREATE TABLE IF NOT EXISTS crawl_runs (
		id          BIGINT PRIMARY KEY,
		registry    VARCHAR,
		started_at  TIMESTAMPTZ,
		finished_at TIMESTAMPTZ
	)`,
	`CREATE TABLE IF NOT EXISTS crawl_repos (
		run_id  BIGINT,
		repo    VARCHAR,
		done_at TIMESTAMPTZ,
		PRIMARY KEY (run_id, repo)
	)`,
	// image_refs lists every string that names an image: its digest,
	// repo@digest, and each repo:tag pointing at it or at its index.
	`CREATE OR REPLACE VIEW image_refs AS
		SELECT digest, list(DISTINCT ref) AS refs, list(DISTINCT ref) FILTER (WHERE kind = 'tag') AS tag_refs
		FROM (
			SELECT digest, digest AS ref, 'digest' AS kind FROM images
			UNION ALL SELECT digest, repo || '@' || digest, 'digest' FROM images
			UNION ALL SELECT t.digest, t.repo || ':' || t.tag, 'tag' FROM tags t JOIN images i ON i.digest = t.digest
			UNION ALL SELECT m.digest, t.repo || ':' || t.tag, 'tag' FROM tags t JOIN index_manifests m ON m.index_digest = t.digest
		)
		GROUP BY digest`,
	// resolve_ref maps a digest, repo@digest or repo:tag to platform
	// manifest digests. An index resolves to every platform it holds.
	`CREATE OR REPLACE MACRO resolve_ref(r) AS TABLE
		SELECT digest FROM images WHERE digest = r OR repo || '@' || digest = r
		UNION
		SELECT coalesce(m.digest, t.digest) AS digest
		FROM tags t LEFT JOIN index_manifests m ON m.index_digest = t.digest
		WHERE t.repo || ':' || t.tag = r OR t.repo || '@' || t.digest = r OR t.digest = r
		UNION
		SELECT id FROM phantoms WHERE id = r`,
	// referrer_kinds says which kinds of artifact are attached to each
	// subject. Artifacts attached to a multi-platform index count for
	// every platform image in it.
	`CREATE OR REPLACE VIEW referrer_kinds AS
		SELECT subject,
			bool_or(kind = 'sbom') AS has_sbom,
			bool_or(kind = 'signature') AS has_signature,
			bool_or(kind = 'attestation') AS has_attestation
		FROM referrers GROUP BY subject`,
	// push_times is each manifest's first push to any repo, from
	// registries that report it.
	`CREATE OR REPLACE VIEW push_times AS
		SELECT digest, min(pushed_at) AS pushed_at FROM pushes GROUP BY digest`,
	// image_view is the flat row the query language compiles against.
	`CREATE OR REPLACE VIEW image_view AS
		SELECT
			i.digest, i.repo, i.media_type, i.index_digest, i.os, i.arch, i.variant,
			i.os || '/' || i.arch || CASE WHEN coalesce(i.variant, '') = '' THEN '' ELSE '/' || i.variant END AS platform,
			i.created,
			CASE WHEN i.created IS NULL OR year(i.created) < 1980 THEN NULL
			     ELSE date_diff('day', i.created, now()) END AS age_days,
			coalesce(pu.pushed_at, pux.pushed_at) AS pushed,
			date_diff('day', coalesce(pu.pushed_at, pux.pushed_at), now()) AS pushed_age_days,
			i.indexed_at AS first_seen,
			i.size, i.layer_count AS layers, i.top_chain_id, i.config_digest, coalesce(i.external, false) AS external,
			i.config_user, i.config_entrypoint, i.config_cmd, i.config_ports, i.config_env_keys,
			i.config_workdir, i.config_stop_signal, i.config_healthcheck,
			l.labels,
			coalesce(r.tag_refs, []::VARCHAR[]) AS tag_refs,
			len(coalesce(r.tag_refs, []::VARCHAR[])) AS tags,
			coalesce(r.refs, [i.digest]) AS refs,
			ln.parent, ln.parent_source, ln.root, coalesce(ln.generation, 0) AS generation,
			coalesce(ln.children, 0) AS children, coalesce(ln.descendants, 0) AS descendants,
			coalesce(ln.conflict, false) AS lineage_conflict,
			coalesce(pr.refs, CASE WHEN ln.parent IS NULL THEN []::VARCHAR[] ELSE [ln.parent] END) AS parent_refs,
			coalesce(rr.refs, CASE WHEN ln.root IS NULL THEN [i.digest] ELSE [ln.root] END) AS root_refs,
			coalesce(ref.has_sbom, false) OR coalesce(refx.has_sbom, false) AS has_sbom,
			coalesce(ref.has_signature, false) OR coalesce(refx.has_signature, false) AS has_signature,
			coalesce(ref.has_attestation, false) OR coalesce(refx.has_attestation, false) AS has_attestation,
			s.verdict AS stale,
			s.reasons AS stale_reasons,
			coalesce(s.base_drift_generations, 0) AS stale_base_drift,
			s.base_drift_days AS stale_base_drift_days,
			s.base_tag AS stale_base_tag,
			s.superseded_days AS stale_superseded_days,
			coalesce(s.untagged, false) AS stale_untagged,
			s.last_pulled_days AS stale_last_pulled_days,
			coalesce(s.eol_base, false) AS stale_eol_base,
			coalesce(s.label_policy, false) AS stale_label_policy,
			coalesce(s.inferred, false) AS stale_inferred,
			coalesce(sc.status = 'complete', false) AS vuln_scanned,
			sc.completed_at AS vuln_scanned_at,
			date_diff('day', sc.completed_at, now()) AS vuln_scan_age_days,
			sc.critical AS vuln_critical, sc.high AS vuln_high, sc.medium AS vuln_medium, sc.low AS vuln_low,
			sc.critical + sc.high + sc.medium + sc.low + sc.other AS vuln_total,
			CASE WHEN sc.findings THEN sc.fixable END AS vuln_fixable
		FROM images i
		LEFT JOIN labels l ON l.digest = i.digest
		LEFT JOIN image_refs r ON r.digest = i.digest
		LEFT JOIN lineage ln ON ln.digest = i.digest
		LEFT JOIN image_refs pr ON pr.digest = ln.parent
		LEFT JOIN image_refs rr ON rr.digest = ln.root
		LEFT JOIN referrer_kinds ref ON ref.subject = i.digest
		LEFT JOIN referrer_kinds refx ON refx.subject = i.index_digest
		LEFT JOIN push_times pu ON pu.digest = i.digest
		LEFT JOIN push_times pux ON pux.digest = i.index_digest
		LEFT JOIN staleness s ON s.digest = i.digest
		LEFT JOIN scans sc ON sc.digest = i.digest AND sc.status = 'complete'`,
}
