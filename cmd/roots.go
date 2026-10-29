package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/render"
	"github.com/rossedman/strata/internal/ui"
)

var rootsCmd = &cobra.Command{
	Use:   "roots",
	Short: "List every root base and how much is built on it",
	Long: `List the root bases images descend from: images with no parent, external bases,
and shared prefixes no indexed image ends at (shown as not in the registry).`,
	Example: `  strata roots
  strata roots --count-descendants
  strata roots -o json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) (err error) {
		v := state.V
		if err := cli.OneOf("output", v.GetString("output"), render.Table, render.JSON, render.CSV, render.NDJSON); err != nil {
			return err
		}
		svc, release, err := state.Backend(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Release(release, &err)
		roots, err := svc.Roots(cmd.Context())
		if err != nil {
			return err
		}
		rows := make([][]any, len(roots))
		total := 0
		for i, r := range roots {
			rows[i] = []any{r.Label, r.ID, r.Kind, int64(r.Children), int64(r.Descendants), r.StalePct}
			total += r.Descendants
		}
		cols := []string{"root", "digest", "kind", "children", "descendants", "stale_pct"}
		if err := render.WriteRows(cli.Out(cmd), v.GetString("output"), cols, rows); err != nil {
			return err
		}
		if v.GetBool("count-descendants") && v.GetString("output") == render.Table {
			ui.Fprint(cli.Out(cmd), ui.Dim.Render(fmt.Sprintf("%d distinct bases with %d descendants\n", len(roots), total)))
		}
		return nil
	},
}

// init registers the roots command and its flags.
func init() {
	rootsCmd.Flags().Bool("count-descendants", false, "print a total line: distinct bases and their descendants")
	rootsCmd.Flags().StringP("output", "o", "table", "output format: table, json, csv or ndjson")
	rootCmd.AddCommand(rootsCmd)
}
