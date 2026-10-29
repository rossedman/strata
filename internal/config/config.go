// Package config defines strata's configuration file, strata.yaml.
//
// Precedence is flag, then environment (STRATA_ prefix, dots and dashes
// become underscores), then file, then the defaults here.
package config

import (
	"fmt"
	"os"
	"path/filepath"

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
		if r.Host == "" {
			return fmt.Errorf("registries[%d].host must be set", i)
		}
		if r.Workers < 0 || r.Rate < 0 {
			return fmt.Errorf("registries[%d]: workers and rate must not be negative", i)
		}
	}
	return nil
}
