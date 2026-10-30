package crawl

import (
	"regexp"
	"slices"
	"sort"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/rossedman/strata/internal/chain"
	"github.com/rossedman/strata/internal/model"
)

// DefaultRedactions match secrets that build arguments leave in history
// commands. Matches are replaced with "***" before anything is stored.
var DefaultRedactions = []string{
	`(?i)((?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credentials?)[A-Za-z0-9_]*\s*[=:]\s*)("[^"]*"|'[^']*'|\S+)`,
	`(?i)(://[^/\s:@]+:)([^@\s]+)(@)`,
	`(?i)(authorization:\s*(?:bearer|basic)\s+)([^\s"']+)`,
}

// Redactor scrubs secrets from build history.
type Redactor struct {
	patterns []*regexp.Regexp
}

// NewRedactor compiles redaction patterns. Each pattern's second capture
// group, or its whole match if it has none, is replaced with "***".
func NewRedactor(patterns []string) (*Redactor, error) {
	r := &Redactor{}
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, err
		}
		r.patterns = append(r.patterns, re)
	}
	return r, nil
}

// Redact returns s with every match replaced.
func (r *Redactor) Redact(s string) string {
	for _, re := range r.patterns {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			sub := re.FindStringSubmatchIndex(m)
			if len(sub) < 6 || sub[4] < 0 {
				return "***"
			}
			return m[:sub[4]] + "***" + m[sub[5]:]
		})
	}
	return s
}

// isImageConfig reports whether a config media type describes a runnable
// image rather than another OCI artifact such as a Helm chart.
func isImageConfig(mt string) bool {
	switch mt {
	case "", "application/vnd.oci.image.config.v1+json", "application/vnd.docker.container.image.v1+json":
		return true
	}
	return false
}

// toImage builds an image record from its manifest and config. Only
// environment variable names are kept, plus the values of allowlisted
// keys; history commands are redacted.
func toImage(digest, repo string, m *v1.Manifest, cfg *v1.ConfigFile, allow []string, red *Redactor) (*model.Image, error) {
	img := &model.Image{
		Digest:       digest,
		Repo:         repo,
		MediaType:    string(m.MediaType),
		ConfigDigest: m.Config.Digest.String(),
		Annotations:  copyMap(m.Annotations),
		Labels:       copyMap(cfg.Config.Labels),
		Platform:     model.Platform{OS: cfg.OS, Arch: cfg.Architecture, Variant: cfg.Variant},
		Created:      cfg.Created.UTC(),
	}
	c := cfg.Config
	img.Config = model.Config{
		User:        c.User,
		Entrypoint:  c.Entrypoint,
		Cmd:         c.Cmd,
		Workdir:     c.WorkingDir,
		StopSignal:  c.StopSignal,
		Healthcheck: c.Healthcheck != nil && len(c.Healthcheck.Test) > 0 && c.Healthcheck.Test[0] != "NONE",
	}
	for p := range c.ExposedPorts {
		img.Config.Ports = append(img.Config.Ports, p)
	}
	sort.Strings(img.Config.Ports)
	for _, kv := range c.Env {
		k, v, _ := strings.Cut(kv, "=")
		img.Config.EnvKeys = append(img.Config.EnvKeys, k)
		if slices.Contains(allow, k) {
			if img.Config.EnvValues == nil {
				img.Config.EnvValues = map[string]string{}
			}
			img.Config.EnvValues[k] = v
		}
	}
	// Each non-empty history entry maps, in order, to one layer.
	var layerCmds []string
	for _, h := range cfg.History {
		cmd := red.Redact(h.CreatedBy)
		img.History = append(img.History, model.HistoryEntry{CreatedBy: cmd, EmptyLayer: h.EmptyLayer})
		if !h.EmptyLayer {
			layerCmds = append(layerCmds, cmd)
		}
	}
	diffIDs := make([]string, len(cfg.RootFS.DiffIDs))
	for i, d := range cfg.RootFS.DiffIDs {
		diffIDs[i] = d.String()
		l := model.Layer{DiffID: diffIDs[i]}
		if i < len(m.Layers) {
			l.BlobDigest = m.Layers[i].Digest.String()
			l.Size = m.Layers[i].Size
			l.MediaType = string(m.Layers[i].MediaType)
			img.Size += l.Size
		}
		if i < len(layerCmds) {
			l.CreatedBy = layerCmds[i]
		}
		img.Layers = append(img.Layers, l)
	}
	ids, err := chain.IDs(diffIDs)
	if err != nil {
		return nil, err
	}
	img.ChainIDs = ids
	return img, nil
}

// copyMap returns a non-nil copy of m.
func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// cosignTag matches the tags cosign uses to store signatures,
// attestations and SBOMs next to an image: sha256-<hex>.sig and so on.
var cosignTag = regexp.MustCompile(`^sha256-([0-9a-f]{64})\.(sig|att|sbom)$`)

// cosignReferrer turns a cosign tag into a referrer of the image it
// names, or reports false if the tag is an ordinary one.
func cosignReferrer(tag, digest string) (model.Referrer, bool) {
	m := cosignTag.FindStringSubmatch(tag)
	if m == nil {
		return model.Referrer{}, false
	}
	kind := map[string]string{"sig": model.KindSignature, "att": model.KindAttestation, "sbom": model.KindSBOM}[m[2]]
	return model.Referrer{Subject: "sha256:" + m[1], Digest: digest, ArtifactType: "cosign/" + m[2], Kind: kind}, true
}
