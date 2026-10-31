package cmd

import (
	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/render"
	"github.com/rossedman/strata/internal/service"
)

var vulnsBasesCmd = &cobra.Command{
	Use:   "bases",
	Short: "Rank the bases that carry the most vulnerabilities to the most images",
	Long: `Lists base images whose scan found vulnerabilities and that other images are
built on, ranked by findings times images in use (pulled within --in-use), then
times images. vulnerable counts the images built on each base whose own scan
also has findings of that severity.`,
	Example: `  strata vulns bases
  strata vulns bases --severity high --in-use 7d
  strata vulns bases -o csv > bases.csv`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) (err error) {
		v := state.V
		if err := cli.OneOf("output", v.GetString("output"), render.Table, render.JSON, render.CSV, render.NDJSON); err != nil {
			return err
		}
		inUse, err := inUseDays()
		if err != nil {
			return err
		}
		svc, release, err := state.Backend(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Release(release, &err)
		bases, err := svc.VulnBases(cmd.Context(), service.VulnBasesOptions{
			Severity: v.GetString("severity"), GroupBy: v.GetString("group-by"), InUseDays: inUse, Limit: v.GetInt("limit"),
		})
		if err != nil {
			return err
		}
		if v.GetString("output") == render.JSON {
			return render.JSONTo(cli.Out(cmd), bases)
		}
		rows := make([][]any, len(bases))
		for i, b := range bases {
			var fixable any
			if b.Fixable != nil {
				fixable = int64(*b.Fixable)
			}
			rows[i] = []any{b.Ref, int64(b.Critical), int64(b.High), fixable, int64(b.Images), int64(b.InUse), int64(b.Vulnerable), int64(b.Groups), b.Digest}
		}
		cols := []string{"base", "critical", "high", "fixable", "images", "in_use", "vulnerable", "groups", "digest"}
		return render.WriteRows(cli.Out(cmd), v.GetString("output"), cols, rows)
	},
}

// init registers the vulns bases command and its flags.
func init() {
	vulnsBasesCmd.Flags().String("severity", "critical", "rank by critical or high findings")
	vulnsBasesCmd.Flags().Int("limit", 20, "number of bases")
	vulnFlags(vulnsBasesCmd)
}
