package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/ui"
)

var historyExportCmd = &cobra.Command{
	Use:     "export <file.parquet>",
	Short:   "Write tag history to a Parquet file",
	Example: `  strata history export tag-history.parquet`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		svc, err := state.Open(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Close(svc, &err)
		n, err := svc.Store.ExportHistory(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		ui.Fprint(cli.Out(cmd), fmt.Sprintf("%s %d tag history rows to %s\n", ui.Title.Render("exported"), n, args[0]))
		return nil
	},
}

// init registers the history export command and its flags.
func init() {
	historyCmd.AddCommand(historyExportCmd)
}
