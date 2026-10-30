package cmd

import (
	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/ui"
)

var refreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Recompute lineage, staleness and today's snapshot without crawling",
	Long: `Recompute the derived views from what is already indexed. Run it after changing
the staleness policy, lineage labels or required labels.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) (err error) {
		svc, err := state.Open(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Close(svc, &err)
		if err := svc.Refresh(cmd.Context()); err != nil {
			return err
		}
		ui.Fprint(cli.Out(cmd), ui.Title.Render("refreshed")+" lineage, staleness and snapshot\n")
		return nil
	},
}

// init registers the refresh command and its flags.
func init() {
	rootCmd.AddCommand(refreshCmd)
}
