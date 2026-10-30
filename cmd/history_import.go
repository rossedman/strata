package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/ui"
)

var historyImportCmd = &cobra.Command{
	Use:   "import <file.parquet>",
	Short: "Merge tag history from a Parquet file written by history export",
	Long: `Merge tag history into the index, keeping rows already present, then recompute
staleness so base drift uses the restored history.`,
	Example: `  strata history import tag-history.parquet`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		svc, err := state.Open(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Close(svc, &err)
		n, err := svc.Store.ImportHistory(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if err := svc.Refresh(cmd.Context()); err != nil {
			return err
		}
		ui.Fprint(cli.Out(cmd), fmt.Sprintf("%s %d new tag history rows from %s\n", ui.Title.Render("imported"), n, args[0]))
		return nil
	},
}

// init registers the history import command and its flags.
func init() {
	historyCmd.AddCommand(historyImportCmd)
}
