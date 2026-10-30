// Package cmd defines strata's commands, one per file.
package cmd

import (
	"context"
	"os"

	"charm.land/fang/v2"
	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/ui"
)

var (
	// cfgFile is the --config flag.
	cfgFile string
	// state is the configuration loaded for the running command.
	state *cli.State
)

var rootCmd = &cobra.Command{
	Use:   "strata",
	Short: "Turn a container registry into a queryable lineage graph",
	Long: `strata indexes every repo, tag, manifest and image config in a registry into a
local DuckDB file, then answers three questions about any image:

  What is it?            labels, config, layers, size and referrers
  Where did it come from? its ancestry, walked back to the root base
  Is it stale?           an explainable verdict with the reasons spelled out

It needs only registry read access: no source repos, no CI, no database server.`,
	Example: `  strata index --registry reg.io
  strata explain reg.io/payments/api:1.8.2
  strata descendants reg.io/base/go:1.22 --all-versions --depth 0
  strata query 'label.team = "payments" and age > 180d'
  strata stale --group-by label.team`,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		s, err := cli.Load(cmd, cfgFile)
		if err != nil {
			return err
		}
		state = s
		return nil
	},
}

// init registers the persistent flags shared by every command.
func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&cfgFile, "config", "", "config file (default: $XDG_CONFIG_HOME/strata/strata.yaml, ~/.strata.yaml or ./strata.yaml)")
	pf.String("db", "", "index file (default: $XDG_DATA_HOME/strata/strata.duckdb)")
	pf.String("log-level", "", "log level: debug, info, warn or error (default: info)")
	pf.String("platform", "", "platform to pick when a ref names a multi-platform index (default: linux/amd64)")
	pf.String("server", "", "URL of a running strata serve to query instead of the index file")
}

// Execute runs the root command through fang, which styles help, usage
// errors and version output.
func Execute() {
	err := fang.Execute(context.Background(), rootCmd,
		fang.WithVersion(cli.Version),
		fang.WithCommit(cli.Commit),
		fang.WithColorSchemeFunc(ui.FangColors),
		fang.WithNotifySignal(os.Interrupt),
	)
	if err != nil {
		os.Exit(1)
	}
}

// Root returns the root command, for tools that walk the command tree
// such as the reference docs generator.
func Root() *cobra.Command { return rootCmd }
