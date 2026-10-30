// Package model holds the records strata indexes: images, tags, layers,
// referrers and lineage edges.
package model

import (
	"strings"
	"time"
)

// Platform identifies the OS and CPU an image runs on.
type Platform struct {
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Variant string `json:"variant,omitempty"`
}

// String formats the platform as os/arch[/variant].
func (p Platform) String() string {
	if p.OS == "" && p.Arch == "" {
		return ""
	}
	s := p.OS + "/" + p.Arch
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// Layer is one entry in an image's layer stack.
type Layer struct {
	DiffID     string `json:"diff_id"`
	BlobDigest string `json:"blob_digest"`
	Size       int64  `json:"size"`
	MediaType  string `json:"media_type"`
	CreatedBy  string `json:"created_by,omitempty"`
}

// HistoryEntry is one build step from the image config's history.
type HistoryEntry struct {
	CreatedBy  string `json:"created_by"`
	EmptyLayer bool   `json:"empty_layer"`
}

// Config holds the run-time fields of an image config that strata indexes
// under config.*. Environment values are never kept, only keys, except for
// keys on the allowlist.
type Config struct {
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

// Image is one platform manifest and its config.
type Image struct {
	Digest       string            `json:"digest"`
	Repo         string            `json:"repo"`
	MediaType    string            `json:"media_type"`
	IndexDigest  string            `json:"index_digest,omitempty"`
	Platform     Platform          `json:"platform"`
	Created      time.Time         `json:"created"`
	Size         int64             `json:"size"`
	ConfigDigest string            `json:"config_digest"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	Config       Config            `json:"config"`
	History      []HistoryEntry    `json:"history,omitempty"`
	Layers       []Layer           `json:"layers"`
	ChainIDs     []string          `json:"chain_ids"`
	External     bool              `json:"external,omitempty"`
}

// TopChainID returns the chain ID of the image's full layer stack.
func (i *Image) TopChainID() string {
	if len(i.ChainIDs) == 0 {
		return ""
	}
	return i.ChainIDs[len(i.ChainIDs)-1]
}

// Metadata merges config labels and manifest annotations into the single
// map strata stores and queries as label.*. Labels win on conflict, because
// they are what the image itself declares.
func (i *Image) Metadata() map[string]string {
	out := make(map[string]string, len(i.Labels)+len(i.Annotations))
	for k, v := range i.Annotations {
		out[k] = v
	}
	for k, v := range i.Labels {
		out[k] = v
	}
	return out
}

// IndexEntry links a multi-platform index to one of its platform manifests.
type IndexEntry struct {
	IndexDigest string
	Digest      string
	Platform    Platform
}

// Tag is where a tag points now.
type Tag struct {
	Repo      string    `json:"repo"`
	Tag       string    `json:"tag"`
	Digest    string    `json:"digest"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Ref formats the tag as repo:tag.
func (t Tag) Ref() string { return t.Repo + ":" + t.Tag }

// TagEvent is one observation of a tag moving, recorded in tag history. An
// empty Digest means the tag was deleted.
type TagEvent struct {
	Repo   string    `json:"repo"`
	Tag    string    `json:"tag"`
	SeenAt time.Time `json:"seen_at"`
	Digest string    `json:"digest"`
	Source string    `json:"source"`
}

// Referrer is an artifact attached to an image, such as an SBOM or a
// signature.
type Referrer struct {
	Subject      string `json:"subject"`
	Digest       string `json:"digest"`
	ArtifactType string `json:"artifact_type"`
	Kind         string `json:"kind"`
}

// Referrer kinds.
const (
	KindSBOM        = "sbom"
	KindSignature   = "signature"
	KindAttestation = "attestation"
	KindOther       = "other"
)

// ClassifyArtifact maps an OCI artifact type to sbom, signature, attestation
// or other.
func ClassifyArtifact(artifactType string) string {
	t := strings.ToLower(artifactType)
	switch {
	case strings.Contains(t, "spdx"), strings.Contains(t, "cyclonedx"), strings.Contains(t, "sbom"):
		return KindSBOM
	case strings.Contains(t, "signature"), strings.Contains(t, "cosign"), strings.Contains(t, "notary"):
		return KindSignature
	case strings.Contains(t, "in-toto"), strings.Contains(t, "intoto"), strings.Contains(t, "attestation"), strings.Contains(t, "dsse"):
		return KindAttestation
	default:
		return KindOther
	}
}

// Edge sources, strongest first.
const (
	SourceLayers      = "layers"
	SourceAttestation = "attestation"
	SourceAnnotation  = "annotation"
	SourceLabel       = "label"
)

// Edge confidences.
const (
	ConfidenceExact    = "exact"
	ConfidenceDeclared = "declared"
	ConfidenceInferred = "inferred"
)

// PhantomPrefix marks a node ID that stands for a deleted ancestor, keyed
// by its chain ID rather than a manifest digest.
const PhantomPrefix = "phantom:"

// IsPhantom reports whether id names a phantom ancestor.
func IsPhantom(id string) bool { return strings.HasPrefix(id, PhantomPrefix) }

// Edge is a child-to-parent lineage link with its provenance.
type Edge struct {
	Child      string `json:"child"`
	Parent     string `json:"parent"`
	Source     string `json:"source"`
	Confidence string `json:"confidence"`
	Evidence   string `json:"evidence"`
}
