// Package cli holds what strata's commands share: configuration loading
// through viper, opening the index, and output helpers. Commands in cmd/
// stay thin and call into it.
package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/rossedman/strata/internal/config"
	"github.com/rossedman/strata/internal/lineage"
	"github.com/rossedman/strata/internal/service"
	"github.com/rossedman/strata/internal/store"
	"github.com/rossedman/strata/internal/ui"
)

// Build information, set with -ldflags at release time.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// configKeys maps flag names to config keys where they differ.
var configKeys = map[string]string{
	"addr":        "serve.addr",
	"auth-header": "serve.auth-header",
	"server":      "serve.url",
}

// ConfigPaths returns the places a config file is looked for, in order:
// $XDG_CONFIG_HOME/strata/strata.yaml, ~/.config/strata/strata.yaml,
// ~/.strata.yaml and ./strata.yaml.
func ConfigPaths() []string {
	var out []string
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		out = append(out, filepath.Join(xdg, "strata", "strata.yaml"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".config", "strata", "strata.yaml"), filepath.Join(home, ".strata.yaml"))
	}
	return append(out, "strata.yaml")
}

// findConfig returns the first config file that exists, or "".
func findConfig() string {
	for _, p := range ConfigPaths() {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// State is the loaded configuration for one command run.
type State struct {
	V      *viper.Viper
	Config config.Config
	Logger *slog.Logger
}

// Load reads the config file, environment and the running command's
// flags. Precedence is flag, then STRATA_* environment, then file, then
// defaults. cfgFile overrides the search path.
func Load(cmd *cobra.Command, cfgFile string) (*State, error) {
	v := viper.New()
	for k, val := range config.Defaults() {
		v.SetDefault(k, val)
	}
	v.SetEnvPrefix("STRATA")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	v.AutomaticEnv()
	if cfgFile == "" {
		cfgFile = findConfig()
	}
	if cfgFile != "" {
		v.SetConfigFile(cfgFile)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
	}
	var bindErr error
	bind := func(f *pflag.Flag) {
		key := f.Name
		if k, ok := configKeys[key]; ok {
			key = k
		}
		if err := v.BindPFlag(key, f); err != nil && bindErr == nil {
			bindErr = err
		}
	}
	cmd.Flags().VisitAll(bind)
	cmd.InheritedFlags().VisitAll(bind)
	if bindErr != nil {
		return nil, bindErr
	}
	st := &State{V: v}
	if err := v.Unmarshal(&st.Config); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := st.Config.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	logger, err := ui.NewLogger(cmd.ErrOrStderr(), st.Config.LogLevel)
	if err != nil {
		return nil, err
	}
	st.Logger = logger
	return st, nil
}

// Open opens the index and returns a service configured from the config.
// The caller closes the store.
func (s *State) Open(ctx context.Context) (*service.Service, error) {
	st, err := store.Open(ctx, s.Config.DB)
	if err != nil {
		if isLocked(err) {
			return nil, fmt.Errorf("%w\nanother strata process (strata serve?) has the index open; point commands at it with --server", err)
		}
		return nil, err
	}
	svc := service.New(st)
	if svc.Policy, err = s.Config.Policy(); err != nil {
		_ = st.Close()
		return nil, err
	}
	svc.Lineage = lineage.Options{Labels: s.Config.LineageLabels}
	svc.Required = s.Config.Required
	svc.Synonyms = s.Config.Synonyms
	if s.Config.Platform != "" {
		svc.Platform = s.Config.Platform
	}
	return svc, nil
}

// Close closes a service's store, keeping the first error.
func Close(svc *service.Service, err *error) {
	if cerr := svc.Store.Close(); cerr != nil && *err == nil {
		*err = cerr
	}
}

// Out returns the command's output writer.
func Out(cmd *cobra.Command) io.Writer { return cmd.OutOrStdout() }

// OneOf checks a flag value against allowed choices.
func OneOf(flag, value string, allowed ...string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("--%s must be one of %s, not %q", flag, strings.Join(allowed, ", "), value)
}

// isLocked reports whether opening the index failed because another
// process holds it.
func isLocked(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Could not set lock")
}
