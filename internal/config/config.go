// Package config defines strata's configuration file, strata.yaml.
//
// Precedence is flag, then environment (STRATA_ prefix, dots and dashes
// become underscores), then file, then the defaults here.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rossedman/strata/internal/acr"
	"github.com/rossedman/strata/internal/crawl"
	"github.com/rossedman/strata/internal/ecr"
	"github.com/rossedman/strata/internal/gar"
	"github.com/rossedman/strata/internal/query"
	"github.com/rossedman/strata/internal/stale"
)

// Registry is one registry to crawl.
type Registry struct {
	// Host is the registry host, such as reg.io.
	Host string `mapstructure:"host"`
	// Repos seeds the repo list for registries whose catalog endpoint is
	// missing or too slow. Empty means use the catalog.
	Repos []string `mapstructure:"repos"`
	// Insecure allows plain HTTP.
	Insecure bool `mapstructure:"insecure"`
	// Workers bounds concurrent requests.
	Workers int `mapstructure:"workers"`
	// Rate is the request limit per second; 0 means unlimited.
	Rate float64 `mapstructure:"rate"`
	// Provider picks how the registry is listed: auto (the default) uses
	// the ECR, Artifact Registry or ACR API for those hosts; ecr, harbor,
	// gar and acr force one; distribution uses the plain distribution API.
	Provider string `mapstructure:"provider"`
	// AWSProfile, AssumeRole and ExternalID pick the AWS identity for an
	// ECR registry, so registries in different accounts can each use
	// their own credentials. Unset, the default AWS credential chain is
	// used.
	AWSProfile string `mapstructure:"aws-profile"`
	AssumeRole string `mapstructure:"assume-role"`
	ExternalID string `mapstructure:"external-id"`
	// Projects are the Google Cloud projects whose Artifact Registry
	// repositories are listed (gar only).
	Projects []string `mapstructure:"projects"`
	// IncludeRepos and ExcludeRepos are repo globs, named without the
	// host: "*" stays within a path segment and "**" crosses them.
	IncludeRepos []string `mapstructure:"include-repos"`
	ExcludeRepos []string `mapstructure:"exclude-repos"`
	// IncludeTags and ExcludeTags are tag globs, such as v* or pr-*.
	IncludeTags []string `mapstructure:"include-tags"`
	ExcludeTags []string `mapstructure:"exclude-tags"`
	// Platforms keeps only these platforms of multi-platform images, such
	// as linux/amd64.
	Platforms []string `mapstructure:"platforms"`
	// PushedSince skips images pushed longer ago than this, such as 90d or
	// 1y. It needs the ECR API.
	PushedSince string `mapstructure:"pushed-since"`
}

// Providers.
const (
	ProviderAuto         = "auto"
	ProviderECR          = "ecr"
	ProviderHarbor       = "harbor"
	ProviderGAR          = "gar"
	ProviderACR          = "acr"
	ProviderDistribution = "distribution"
)

// Kind returns the provider the registry is listed with, resolving auto
// from the host: ECR, Artifact Registry and ACR hosts are recognized;
// Harbor has to be named.
func (r Registry) Kind() string {
	switch r.Provider {
	case "", ProviderAuto:
	default:
		return r.Provider
	}
	if _, _, ok := ecr.ParseHost(r.Host); ok {
		return ProviderECR
	}
	if _, ok := gar.ParseHost(r.Host); ok {
		return ProviderGAR
	}
	if acr.IsHost(r.Host) {
		return ProviderACR
	}
	return ProviderDistribution
}

// UsesECR reports whether the registry is listed through the ECR API.
func (r Registry) UsesECR() bool { return r.Kind() == ProviderECR }

// ReportsPushes reports whether the registry's API reports push times.
func (r Registry) ReportsPushes() bool { return r.Kind() != ProviderDistribution }

// Check reports settings that would fail the crawl.
func (r Registry) Check() error {
	if r.Host == "" {
		return fmt.Errorf("host must be set")
	}
	if r.Workers < 0 || r.Rate < 0 {
		return fmt.Errorf("workers and rate must not be negative")
	}
	switch r.Provider {
	case "", ProviderAuto, ProviderECR, ProviderHarbor, ProviderGAR, ProviderACR, ProviderDistribution:
	default:
		return fmt.Errorf("provider %q: use auto, ecr, harbor, gar, acr or distribution", r.Provider)
	}
	if r.Provider == ProviderECR {
		if _, _, ok := ecr.ParseHost(r.Host); !ok {
			return fmt.Errorf("provider ecr needs an ECR host, such as 123456789012.dkr.ecr.us-east-1.amazonaws.com")
		}
	}
	if (r.AWSProfile != "" || r.AssumeRole != "" || r.ExternalID != "") && r.Kind() != ProviderECR {
		return fmt.Errorf("aws-profile, assume-role and external-id apply to ECR registries only")
	}
	if r.ExternalID != "" && r.AssumeRole == "" {
		return fmt.Errorf("external-id needs assume-role")
	}
	if r.Kind() == ProviderGAR {
		if _, ok := gar.ParseHost(r.Host); !ok {
			return fmt.Errorf("provider gar needs an Artifact Registry host, such as us-docker.pkg.dev")
		}
		if len(r.Projects) == 0 && len(r.Repos) == 0 {
			return fmt.Errorf("artifact registry needs projects, or repos as project/repository/image")
		}
	}
	if _, err := r.Filter(); err != nil {
		return err
	}
	if r.PushedSince != "" && !r.ReportsPushes() {
		return fmt.Errorf("pushed-since needs a registry API that reports push times: ECR, Harbor, Artifact Registry or ACR")
	}
	return nil
}

// Filter returns the registry's crawl filter.
func (r Registry) Filter() (crawl.Filter, error) {
	f := crawl.Filter{
		IncludeRepos: r.IncludeRepos, ExcludeRepos: r.ExcludeRepos,
		IncludeTags: r.IncludeTags, ExcludeTags: r.ExcludeTags,
		Platforms: r.Platforms,
	}
	if r.PushedSince != "" {
		days, err := query.ParseDays(r.PushedSince)
		if err != nil {
			return f, fmt.Errorf("pushed-since: %w", err)
		}
		f.PushedSince = time.Duration(days * float64(24*time.Hour))
	}
	return f, f.Validate()
}

// Serve configures strata serve.
type Serve struct {
	// Addr is the listen address. Anything but loopback is shared mode,
	// which refuses to start without auth.
	Addr string `mapstructure:"addr"`
	// AuthHeader is a header set by a trusted auth proxy, such as
	// X-Forwarded-User. Requests without it are rejected.
	AuthHeader string `mapstructure:"auth-header"`
	// URL is where a running server is reached; CLI commands route through
	// it when the index file is locked by the server.
	URL string `mapstructure:"url"`
}

// Config is the whole configuration.
type Config struct {
	// DB is the index file.
	DB         string     `mapstructure:"db"`
	Registries []Registry `mapstructure:"registries"`
	// External bases fetched so roots resolve, such as
	// docker.io/library/debian:bookworm-slim.
	External []string `mapstructure:"external"`
	// Referrers fetches SBOMs, signatures and attestations.
	Referrers bool `mapstructure:"referrers"`
	// Platform is the default when a ref names a multi-platform index.
	Platform string `mapstructure:"platform"`
	// LineageLabels are label keys naming an image's base.
	LineageLabels []string `mapstructure:"lineage-labels"`
	// PolicyFile is a staleness policy YAML file.
	PolicyFile string `mapstructure:"policy"`
	// Required label keys; missing:<key> queries and compliance use them.
	Required []string `mapstructure:"required"`
	// Synonyms map label keys that mean the same, such as owner: team.
	Synonyms map[string]string `mapstructure:"synonyms"`
	// EnvAllowlist names environment variables whose values are stored.
	EnvAllowlist []string `mapstructure:"env-allowlist"`
	// Redactions are extra regular expressions scrubbed from history.
	Redactions []string `mapstructure:"redactions"`
	Serve      Serve    `mapstructure:"serve"`
	LogLevel   string   `mapstructure:"log-level"`
}

// DefaultDB returns the default index path:
// $XDG_DATA_HOME/strata/strata.duckdb, or ~/.local/share/strata.
func DefaultDB() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "strata.duckdb"
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "strata", "strata.duckdb")
}

// Defaults returns the configuration used when nothing is set.
func Defaults() map[string]any {
	// Every key is listed, even with a zero value, so viper's environment
	// lookup sees it when unmarshaling.
	return map[string]any{
		"db":                DefaultDB(),
		"external":          []string{},
		"referrers":         true,
		"platform":          "linux/amd64",
		"lineage-labels":    []string{},
		"policy":            "",
		"required":          []string{},
		"env-allowlist":     []string{},
		"redactions":        []string{},
		"log-level":         "info",
		"serve.addr":        "127.0.0.1:7878",
		"serve.auth-header": "",
		"serve.url":         "",
	}
}

// Policy loads the staleness policy the config names, or the defaults.
func (c *Config) Policy() (stale.Policy, error) {
	return stale.LoadPolicy(c.PolicyFile)
}

// Validate checks values that would otherwise fail late.
func (c *Config) Validate() error {
	if c.DB == "" {
		return fmt.Errorf("db must be set")
	}
	for i, r := range c.Registries {
		if err := r.Check(); err != nil {
			return fmt.Errorf("registries[%d]: %w", i, err)
		}
	}
	return nil
}
