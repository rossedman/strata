package cmd

import (
	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/render"
)

var staleCmd = &cobra.Command{
	Use:   "stale",
	Short: "Report the staleness mix, optionally per group",
	Long: `Count images per verdict: fresh, drifting, stale and abandoned. Verdicts come from
the policy file; see strata explain for the reasons behind any one image.`,
	Example: `  strata stale
  strata stale --group-by label.team
  strata stale --group-by root -o csv`,
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
		t, err := svc.Stale(cmd.Context(), v.GetString("group-by"))
		if err != nil {
			return err
		}
		return render.WriteRows(cli.Out(cmd), v.GetString("output"), t.Columns, t.Rows)
	},
}

// init registers the stale command and its flags.
func init() {
	staleCmd.Flags().String("group-by", "", "group by repo, root or any field such as label.team")
	staleCmd.Flags().StringP("output", "o", "table", "output format: table, json, csv or ndjson")
	rootCmd.AddCommand(staleCmd)
}
