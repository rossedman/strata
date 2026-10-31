package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/rossedman/strata/internal/graph"
	"github.com/rossedman/strata/internal/model"
)

// ImageInfo is what the index knows about one image.
type ImageInfo struct {
	Digest      string     `json:"digest"`
	Repo        string     `json:"repo"`
	Label       string     `json:"label"`
	Tags        []string   `json:"tags"`
	IndexDigest string     `json:"index_digest,omitempty"`
	Platform    string     `json:"platform"`
	Platforms   []string   `json:"platforms,omitempty"`
	Created     *time.Time `json:"created,omitempty"`
	AgeDays     *int       `json:"age_days,omitempty"`
	// Pushed is when the registry says the image was first pushed (ECR);
	// FirstSeen is when strata first indexed it.
	Pushed     *time.Time        `json:"pushed,omitempty"`
	FirstSeen  *time.Time        `json:"first_seen,omitempty"`
	Size       int64             `json:"size"`
	LayerCount int               `json:"layer_count"`
	TopChainID string            `json:"top_chain_id"`
	External   bool              `json:"external"`
	Labels     map[string]string `json:"labels"`
	Config     ConfigInfo        `json:"config"`
}

// ConfigInfo holds the run-time fields from the image config.
type ConfigInfo struct {
	User        string            `json:"user"`
	Entrypoint  []string          `json:"entrypoint"`
	Cmd         []string          `json:"cmd"`
	Ports       []string          `json:"ports"`
	EnvKeys     []string          `json:"env_keys"`
	EnvValues   map[string]string `json:"env_values,omitempty"`
	Workdir     string            `json:"workdir"`
	StopSignal  string            `json:"stop_signal"`
	Healthcheck bool              `json:"healthcheck"`
}

// LayerInfo is one layer and the build step that made it.
type LayerInfo struct {
	Position   int    `json:"position"`
	DiffID     string `json:"diff_id"`
	BlobDigest string `json:"blob_digest"`
	Size       int64  `json:"size"`
	CreatedBy  string `json:"created_by"`
	// SharedBy is how many indexed images contain this layer.
	SharedBy int `json:"shared_by"`
	// AddedBy is the ancestor whose build added this layer: an image
	// digest, or a phantom id for a base that is not indexed. Empty means
	// the image's own build added it.
	AddedBy string `json:"added_by,omitempty"`
}

// StalenessInfo is an image's verdict and the signals behind it.
type StalenessInfo struct {
	Verdict              string   `json:"verdict"`
	Reasons              []string `json:"reasons"`
	BaseDriftGenerations int      `json:"base_drift_generations"`
	BaseTag              string   `json:"base_tag,omitempty"`
	SupersededDays       *int     `json:"superseded_days,omitempty"`
	Untagged             bool     `json:"untagged"`
	Inferred             bool     `json:"inferred"`
}

// LineageInfo summarizes where an image sits in the graph.
type LineageInfo struct {
	Status      string       `json:"status"`
	Parent      string       `json:"parent,omitempty"`
	Root        string       `json:"root,omitempty"`
	Generation  int          `json:"generation"`
	Children    int          `json:"children"`
	Descendants int          `json:"descendants"`
	Conflict    bool         `json:"conflict"`
	Edges       []graph.Edge `json:"edges"`
	// BuiltFrom are the images the build's provenance says it pulled,
	// with whether each is indexed, so a missing parent can be explained.
	BuiltFrom []BuiltFrom  `json:"built_from,omitempty"`
	Ancestry  *graph.Graph `json:"ancestry"`
}

// Explanation answers "what is this image, and where did it come from?".
type Explanation struct {
	Image     ImageInfo            `json:"image"`
	Lineage   LineageInfo          `json:"lineage"`
	Staleness StalenessInfo        `json:"staleness"`
	Layers    []LayerInfo          `json:"layers"`
	History   []model.HistoryEntry `json:"history,omitempty"`
	Referrers []model.Referrer     `json:"referrers"`
	Findings  []string             `json:"findings"`
}

// secretKeyGlobs flag environment variable names that look like they
// carry a secret. Only the name is ever stored.
var secretKeyGlobs = []string{"*SECRET*", "*TOKEN*", "*PASSWORD*", "*PASSWD*", "*API_KEY*", "*ACCESS_KEY*", "*PRIVATE_KEY*", "*CREDENTIAL*"}

// Explain gathers everything known about one image.
func (s *Service) Explain(ctx context.Context, ref, platform string, history bool) (*Explanation, error) {
	digest, err := s.Resolve(ctx, ref, platform)
	if err != nil {
		return nil, err
	}
	ex := &Explanation{Referrers: []model.Referrer{}, Findings: []string{}}
	if err := s.imageInfo(ctx, digest, &ex.Image); err != nil {
		return nil, err
	}
	if err := s.lineageInfo(ctx, digest, &ex.Lineage); err != nil {
		return nil, err
	}
	if err := s.stalenessInfo(ctx, digest, &ex.Staleness); err != nil {
		return nil, err
	}
	if ex.Layers, err = s.layers(ctx, digest); err != nil {
		return nil, err
	}
	if err := s.layerOrigins(ctx, digest, ex.Layers); err != nil {
		return nil, err
	}
	if history {
		if ex.History, err = s.history(ctx, digest); err != nil {
			return nil, err
		}
	}
	if ex.Referrers, err = s.referrers(ctx, digest, ex.Image.IndexDigest); err != nil {
		return nil, err
	}
	ex.Findings = findings(ex)
	return ex, nil
}

// imageInfo fills in an image's own fields.
func (s *Service) imageInfo(ctx context.Context, digest string, im *ImageInfo) error {
	var created, pushed, firstSeen sql.NullTime
	var age sql.NullInt64
	var index, top sql.NullString
	var labels string
	var tags, entry, cmd, ports, envKeys any
	err := s.db().QueryRowContext(ctx, `
		SELECT digest, repo, coalesce(index_digest, ''), platform, created, age_days, coalesce(size, 0), coalesce(layers, 0),
			top_chain_id, external, coalesce(labels::JSON::VARCHAR, '{}'), tag_refs,
			coalesce(config_user, ''), config_entrypoint, config_cmd, config_ports, config_env_keys,
			coalesce(config_workdir, ''), coalesce(config_stop_signal, ''), coalesce(config_healthcheck, false),
			pushed, first_seen
		FROM `+s.view()+` WHERE digest = ?`, digest).Scan(
		&im.Digest, &im.Repo, &index, &im.Platform, &created, &age, &im.Size, &im.LayerCount,
		&top, &im.External, &labels, &tags,
		&im.Config.User, &entry, &cmd, &ports, &envKeys,
		&im.Config.Workdir, &im.Config.StopSignal, &im.Config.Healthcheck,
		&pushed, &firstSeen)
	if err != nil {
		return fmt.Errorf("read image %s: %w", digest, err)
	}
	im.IndexDigest, im.TopChainID = index.String, top.String
	if created.Valid && created.Time.Year() >= 1980 {
		t := created.Time.UTC()
		im.Created = &t
	}
	if age.Valid {
		a := int(age.Int64)
		im.AgeDays = &a
	}
	im.Pushed, im.FirstSeen = utcTime(pushed), utcTime(firstSeen)
	im.Tags = toStrings(tags)
	im.Label = graph.Label(im.Repo, im.Digest, im.Tags)
	im.Labels = map[string]string{}
	var raw map[string]any
	if err := json.Unmarshal([]byte(labels), &raw); err == nil {
		for k, v := range raw {
			im.Labels[k] = fmt.Sprint(v)
		}
	}
	im.Config.Entrypoint = unsorted(entry)
	im.Config.Cmd = unsorted(cmd)
	im.Config.Ports = toStrings(ports)
	im.Config.EnvKeys = unsorted(envKeys)
	rows, err := s.db().QueryContext(ctx, `SELECT key, value FROM env_values WHERE digest = ? ORDER BY key`, digest)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		if im.Config.EnvValues == nil {
			im.Config.EnvValues = map[string]string{}
		}
		im.Config.EnvValues[k] = v
	}
	if im.IndexDigest != "" {
		im.Platforms, err = s.strings(ctx, `SELECT os || '/' || arch || CASE WHEN coalesce(variant, '') = '' THEN '' ELSE '/' || variant END
			FROM index_manifests WHERE index_digest = ? ORDER BY 1`, im.IndexDigest)
		if err != nil {
			return err
		}
	}
	return rows.Err()
}

// unsorted converts a scanned list to strings, keeping order.
func unsorted(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

// BuiltFrom is one image a build pulled, by its provenance.
type BuiltFrom struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest,omitempty"`
	// Status is parent (the image is built on it), stage (indexed, but
	// another build stage's image: the image does not start with its
	// layers), missing (strata could not fetch it; Reason says why) or
	// pending (the next index fetches it).
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// builtFrom reads an image's provenance bases and where each stands.
func (s *Service) builtFrom(ctx context.Context, digest string) ([]BuiltFrom, error) {
	rows, err := s.db().QueryContext(ctx, `
		SELECT b.ref, b.base_digest,
			EXISTS (SELECT 1 FROM edges e WHERE e.child = b.digest AND e.source = 'attestation'
				AND (e.parent = b.base_digest OR e.parent IN (SELECT digest FROM index_manifests WHERE index_digest = b.base_digest))),
			b.base_digest IN (SELECT digest FROM images) OR b.base_digest IN (SELECT index_digest FROM index_manifests),
			f.error
		FROM base_refs b LEFT JOIN base_fetches f ON f.base_digest = b.base_digest
		WHERE b.digest = ? ORDER BY b.ref`, digest)
	if err != nil {
		return nil, fmt.Errorf("read provenance: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []BuiltFrom
	for rows.Next() {
		var b BuiltFrom
		var parent, indexed bool
		var fetchErr sql.NullString
		if err := rows.Scan(&b.Ref, &b.Digest, &parent, &indexed, &fetchErr); err != nil {
			return nil, err
		}
		switch {
		case parent:
			b.Status = "parent"
		case indexed:
			b.Status = "stage"
		case fetchErr.Valid:
			b.Status, b.Reason = "missing", fetchErr.String
		default:
			b.Status = "pending"
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// lineageInfo fills in the parent chain and every edge with provenance.
func (s *Service) lineageInfo(ctx context.Context, digest string, li *LineageInfo) error {
	var parent, root sql.NullString
	err := s.db().QueryRowContext(ctx, `
		SELECT parent, root, coalesce(generation, 0), coalesce(children, 0), coalesce(descendants, 0), coalesce(conflict, false)
		FROM lineage WHERE digest = ?`, digest).Scan(&parent, &root, &li.Generation, &li.Children, &li.Descendants, &li.Conflict)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read lineage: %w", err)
	}
	li.Parent, li.Root = parent.String, root.String
	edges, err := s.edges(ctx, []string{digest})
	if err != nil {
		return err
	}
	li.Edges = edges
	if li.BuiltFrom, err = s.builtFrom(ctx, digest); err != nil {
		return err
	}
	g, err := s.ancestryGraph(ctx, digest)
	if err != nil {
		return err
	}
	li.Ancestry = g
	var layers int
	_ = s.db().QueryRowContext(ctx, `SELECT coalesce(layer_count, 0) FROM images WHERE digest = ?`, digest).Scan(&layers)
	switch {
	case li.Parent != "":
		li.Status = "derived"
	case li.Descendants > 0:
		li.Status = "root"
	case layers <= 1:
		li.Status = "unknown"
	default:
		li.Status = "root"
	}
	return nil
}

// edges returns every edge whose child is one of ids.
func (s *Service) edges(ctx context.Context, ids []string) ([]graph.Edge, error) {
	rows, err := s.db().QueryContext(ctx, `
		SELECT e.parent, e.child, e.source, e.confidence, coalesce(l.conflict, false) AND e.source <> 'layers'
		FROM edges e LEFT JOIN lineage l ON l.digest = e.child
		WHERE list_contains(?, e.child)
		ORDER BY e.child, CASE e.source WHEN 'layers' THEN 1 WHEN 'attestation' THEN 2 WHEN 'annotation' THEN 3 ELSE 4 END`, ids)
	if err != nil {
		return nil, fmt.Errorf("read edges: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []graph.Edge{}
	for rows.Next() {
		var e graph.Edge
		if err := rows.Scan(&e.Parent, &e.Child, &e.Source, &e.Confidence, &e.Conflict); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// stalenessInfo fills in the verdict and reasons.
func (s *Service) stalenessInfo(ctx context.Context, digest string, si *StalenessInfo) error {
	var reasons string
	var superseded sql.NullInt64
	var baseTag sql.NullString
	err := s.db().QueryRowContext(ctx, `
		SELECT verdict, coalesce(reasons::JSON::VARCHAR, '[]'), coalesce(base_drift_generations, 0), base_tag,
			superseded_days, coalesce(untagged, false), coalesce(inferred, false)
		FROM staleness WHERE digest = ?`, digest).Scan(&si.Verdict, &reasons, &si.BaseDriftGenerations, &baseTag,
		&superseded, &si.Untagged, &si.Inferred)
	if err == sql.ErrNoRows {
		si.Reasons = []string{}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read staleness: %w", err)
	}
	si.BaseTag = baseTag.String
	if superseded.Valid {
		d := int(superseded.Int64)
		si.SupersededDays = &d
	}
	si.Reasons = []string{}
	return json.Unmarshal([]byte(reasons), &si.Reasons)
}

// layers lists an image's layers with how widely each is shared.
func (s *Service) layers(ctx context.Context, digest string) ([]LayerInfo, error) {
	rows, err := s.db().QueryContext(ctx, `
		SELECT l.position, l.diff_id, coalesce(l.blob_digest, ''), coalesce(l.size, 0), coalesce(l.created_by, ''),
			(SELECT count(DISTINCT x.digest) FROM image_layers x WHERE x.diff_id = l.diff_id)
		FROM image_layers l WHERE l.digest = ? ORDER BY l.position`, digest)
	if err != nil {
		return nil, fmt.Errorf("read layers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []LayerInfo{}
	for rows.Next() {
		var l LayerInfo
		if err := rows.Scan(&l.Position, &l.DiffID, &l.BlobDigest, &l.Size, &l.CreatedBy, &l.SharedBy); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// layerOrigins sets which ancestor added each layer: the nearest one
// whose layers reach that far. An ancestor's layers are this image's
// first n, where n is the depth at which its top chain ID appears among
// this image's prefixes.
func (s *Service) layerOrigins(ctx context.Context, digest string, layers []LayerInfo) error {
	rows, err := s.db().QueryContext(ctx, `
		WITH RECURSIVE up(id, d) AS (
			SELECT parent, 1 FROM lineage WHERE digest = ? AND parent IS NOT NULL
			UNION ALL
			SELECT l.parent, up.d + 1 FROM up JOIN lineage l ON l.digest = up.id
			WHERE l.parent IS NOT NULL AND up.d < 64
		)
		SELECT up.id, p.depth FROM up
		LEFT JOIN images i ON i.digest = up.id
		JOIN image_prefixes p ON p.digest = ? AND p.chain_id = coalesce(i.top_chain_id,
			CASE WHEN starts_with(up.id, '`+model.PhantomPrefix+`') THEN substr(up.id, `+fmt.Sprint(len(model.PhantomPrefix)+1)+`) END)
		ORDER BY p.depth`, digest, digest)
	if err != nil {
		return fmt.Errorf("read layer origins: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type reach struct {
		id    string
		depth int
	}
	var ancestors []reach
	for rows.Next() {
		var r reach
		if err := rows.Scan(&r.id, &r.depth); err != nil {
			return err
		}
		ancestors = append(ancestors, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range layers {
		for _, a := range ancestors {
			if layers[i].Position <= a.depth {
				layers[i].AddedBy = a.id
				break
			}
		}
	}
	return nil
}

// history returns the build steps from the image config.
func (s *Service) history(ctx context.Context, digest string) ([]model.HistoryEntry, error) {
	rows, err := s.db().QueryContext(ctx, `SELECT coalesce(created_by, ''), coalesce(empty_layer, false)
		FROM image_history WHERE digest = ? ORDER BY position`, digest)
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []model.HistoryEntry{}
	for rows.Next() {
		var h model.HistoryEntry
		if err := rows.Scan(&h.CreatedBy, &h.EmptyLayer); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// referrers lists artifacts attached to the image or its index.
func (s *Service) referrers(ctx context.Context, digest, index string) ([]model.Referrer, error) {
	rows, err := s.db().QueryContext(ctx, `SELECT subject, digest, coalesce(artifact_type, ''), kind
		FROM referrers WHERE subject = ? OR subject = ? ORDER BY kind, digest`, digest, index)
	if err != nil {
		return nil, fmt.Errorf("read referrers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []model.Referrer{}
	for rows.Next() {
		var r model.Referrer
		if err := rows.Scan(&r.Subject, &r.Digest, &r.ArtifactType, &r.Kind); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// findings lists things worth a look: secret-looking environment keys,
// running as root, shell-wrapper entrypoints, a missing healthcheck and
// lineage conflicts.
func findings(ex *Explanation) []string {
	out := []string{}
	for _, k := range ex.Image.Config.EnvKeys {
		for _, g := range secretKeyGlobs {
			if ok, _ := path.Match(g, strings.ToUpper(k)); ok {
				out = append(out, fmt.Sprintf("environment key %s looks like a secret (its value is not stored)", k))
				break
			}
		}
	}
	switch ex.Image.Config.User {
	case "", "0", "root", "0:0", "root:root":
		out = append(out, "runs as root")
	}
	if ep := ex.Image.Config.Entrypoint; len(ep) > 0 && (strings.HasSuffix(ep[0], "/sh") || strings.HasSuffix(ep[0], "/bash")) {
		out = append(out, "entrypoint starts through a shell: "+strings.Join(ep, " "))
	}
	if !ex.Image.Config.Healthcheck {
		out = append(out, "no healthcheck")
	}
	if ex.Lineage.Conflict {
		out = append(out, "declared parent disagrees with the parent inferred from layers")
	}
	if ex.Lineage.Status == "unknown" {
		out = append(out, "lineage unknown: one layer and no declared base (squashed, or FROM scratch)")
	}
	sort.Strings(out)
	return out
}

// utcTime returns a nullable time in UTC, or nil.
func utcTime(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}
