package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/render"
	"github.com/rossedman/strata/internal/store"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version, commit, build date and index format",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		info := map[string]any{
			"version": cli.Version, "commit": cli.Commit, "date": cli.Date,
			"index_format": store.FormatVersion,
		}
		if state.V.GetString("output") == render.JSON {
			return render.JSONTo(cli.Out(cmd), info)
		}
		_, err := fmt.Fprintf(cli.Out(cmd), "strata %s\ncommit %s\nbuilt %s\nindex format %d\n", cli.Version, cli.Commit, cli.Date, store.FormatVersion)
		return err
	},
}

// init registers the version command and its flags.
func init() {
	versionCmd.Flags().StringP("output", "o", "text", "output format: text or json")
	rootCmd.AddCommand(versionCmd)
}
