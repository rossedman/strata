package cmd

import (
	"fmt"
	"net"

	"github.com/spf13/cobra"
)

var uiCmd = &cobra.Command{
	Use:   "ui",
	Short: "Open the web UI on this machine",
	Long: `Serve the web UI for your own index on localhost, with no auth. It is strata serve
restricted to a loopback address; use strata serve with --auth-header to share one
index with a team.`,
	Example: `  strata ui
  strata ui --addr 127.0.0.1:9000`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		addr := state.Config.Serve.Addr
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("--addr %q: %w", addr, err)
		}
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("strata ui only binds to localhost; use strata serve --auth-header to share the UI")
		}
		state.Config.Serve.AuthHeader = ""
		return serveCmd.RunE(cmd, nil)
	},
}

// init registers the ui command and its flags.
func init() {
	uiCmd.Flags().String("addr", "127.0.0.1:7878", "loopback address to listen on")
	uiCmd.Flags().Duration("reindex-every", 0, "re-crawl the configured registries this often, such as 15m (0 disables)")
	rootCmd.AddCommand(uiCmd)
}
