// Package stale gives every image an explainable staleness verdict.
//
// Signals (base drift, supersession, untagged, age, pulls, EOL roots and
// label policy) combine into one of four verdicts through a policy file.
// Every verdict carries the reasons that produced it; there is no opaque
// score.
package stale

import (
	"fmt"
	"os"
	"path"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Verdicts, from best to worst.
const (
	Fresh     = "fresh"
	Drifting  = "drifting"
	Stale     = "stale"
	Abandoned = "abandoned"
)

// Verdicts lists every verdict in display order.
var Verdicts = []string{Fresh, Drifting, Stale, Abandoned}

// Thresholds are the numbers a verdict is decided by. Zero values in an
// override leave the base policy's value in place.
type Thresholds struct {
	// StaleGenerations is how many generations of base drift make an
	// image stale. One generation less makes it drifting.
	StaleGenerations int `yaml:"stale_generations"`
	// SupersededDays is how long an image can be superseded under a tag
	// before it is stale rather than drifting.
	SupersededDays int `yaml:"superseded_days"`
	// AbandonedPullDays is how long an untagged leaf can go unpulled
	// before it is abandoned.
	AbandonedPullDays int `yaml:"abandoned_pull_days"`
}

// Override applies different thresholds to images matching a repo glob
// and label values.
type Override struct {
	Repo       string            `yaml:"repo"`
	Labels     map[string]string `yaml:"labels"`
	Thresholds Thresholds        `yaml:"thresholds"`
}

// Policy configures how signals become verdicts.
type Policy struct {
	Thresholds Thresholds `yaml:"thresholds"`
	// DeprecatedLabels are label keys that mark an image stale when set to
	// a true value (true, yes, 1).
	DeprecatedLabels []string `yaml:"deprecated_labels"`
	// ExpiresLabels are label keys holding a date (YYYY-MM-DD or RFC 3339)
	// after which the image is stale.
	ExpiresLabels []string `yaml:"expires_labels"`
	// EOLRoots are root refs or digests, with * globs, whose OS is past
	// end of life, such as docker.io/library/debian:buster*.
	EOLRoots []string `yaml:"eol_roots"`
	// AssumeUnpulled treats images as unpulled when the registry gives no
	// pull data, so untagged leaves can still be called abandoned.
	AssumeUnpulled *bool      `yaml:"assume_unpulled"`
	Overrides      []Override `yaml:"overrides"`
}

// DefaultPolicy returns the starting thresholds from the spec. They are
// defaults to tune, not recommendations.
func DefaultPolicy() Policy {
	yes := true
	return Policy{
		Thresholds:       Thresholds{StaleGenerations: 2, SupersededDays: 30, AbandonedPullDays: 90},
		DeprecatedLabels: []string{"deprecated"},
		ExpiresLabels:    []string{"expires"},
		AssumeUnpulled:   &yes,
	}
}

// LoadPolicy reads a policy file over the defaults. An empty path returns
// the defaults.
func LoadPolicy(file string) (Policy, error) {
	p := DefaultPolicy()
	if file == "" {
		return p, nil
	}
	data, err := os.ReadFile(file) // #nosec G304 -- the user names their own policy file.
	if err != nil {
		return p, fmt.Errorf("read policy: %w", err)
	}
	return ParsePolicy(data)
}

// ParsePolicy parses policy YAML over the defaults and validates it.
func ParsePolicy(data []byte) (Policy, error) {
	p := DefaultPolicy()
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil && err.Error() != "EOF" {
		return p, fmt.Errorf("parse policy: %w", err)
	}
	if err := p.Validate(); err != nil {
		return p, err
	}
	return p, nil
}

// Validate checks that thresholds are usable and globs are well formed.
func (p Policy) Validate() error {
	t := p.Thresholds
	if t.StaleGenerations < 1 || t.SupersededDays < 0 || t.AbandonedPullDays < 0 {
		return fmt.Errorf("policy thresholds must be positive: %+v", t)
	}
	globs := append([]string{}, p.EOLRoots...)
	for _, o := range p.Overrides {
		globs = append(globs, o.Repo)
	}
	for _, g := range globs {
		if _, err := path.Match(g, ""); err != nil {
			return fmt.Errorf("bad glob %q: %w", g, err)
		}
	}
	return nil
}

// For returns the thresholds that apply to an image, with matching
// overrides applied in order.
func (p Policy) For(repo string, labels map[string]string) Thresholds {
	t := p.Thresholds
	for _, o := range p.Overrides {
		if !o.matches(repo, labels) {
			continue
		}
		if o.Thresholds.StaleGenerations > 0 {
			t.StaleGenerations = o.Thresholds.StaleGenerations
		}
		if o.Thresholds.SupersededDays > 0 {
			t.SupersededDays = o.Thresholds.SupersededDays
		}
		if o.Thresholds.AbandonedPullDays > 0 {
			t.AbandonedPullDays = o.Thresholds.AbandonedPullDays
		}
	}
	return t
}

// matches reports whether an override applies to an image.
func (o Override) matches(repo string, labels map[string]string) bool {
	if o.Repo != "" {
		if ok, _ := path.Match(o.Repo, repo); !ok {
			return false
		}
	}
	for k, v := range o.Labels {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// globMatch reports whether any pattern matches any of the refs.
func globMatch(patterns, refs []string) (string, bool) {
	for _, p := range patterns {
		for _, r := range refs {
			if ok, _ := path.Match(p, r); ok {
				return r, true
			}
		}
	}
	return "", false
}
