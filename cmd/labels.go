package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/render"
	"github.com/rossedman/strata/internal/ui"
)

var labelsCmd = &cobra.Command{
	Use:   "labels [key]",
	Short: "Profile label and annotation keys: coverage, values and near-duplicates",
	Long: `Without a key, list every label and annotation key with its coverage, number of
distinct values and top values, which separates enums like env from free text like
commit. With a key, show its value counts, the repos lacking it, and when it was
first and last seen. --similar finds near-duplicate keys (team, Team, team_name, and
synonyms from the config) so they can be unified at the source; strata never
rewrites anything.`,
	Example: `  strata labels
  strata labels team
  strata labels --similar`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		v := state.V
		out := v.GetString("output")
		if err := cli.OneOf("output", out, render.Table, render.JSON, render.CSV, render.NDJSON); err != nil {
			return err
		}
		svc, err := state.Open(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Close(svc, &err)
		w := cli.Out(cmd)
		switch {
		case v.GetBool("similar"):
			groups, err := svc.SimilarKeys(cmd.Context())
			if err != nil {
				return err
			}
			if out == render.JSON {
				return render.JSONTo(w, groups)
			}
			rows := make([][]any, len(groups))
			for i, g := range groups {
				rows[i] = []any{g}
			}
			return render.WriteRows(w, out, []string{"similar_keys"}, rows)
		case len(args) == 1:
			d, err := svc.Label(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if out == render.JSON {
				return render.JSONTo(w, d)
			}
			rows := [][]any{}
			for val, n := range d.Values {
				rows = append(rows, []any{val, n})
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i][1].(int64) > rows[j][1].(int64) })
			if err := render.WriteRows(w, out, []string{"value", "images"}, rows); err != nil {
				return err
			}
			if out != render.Table {
				return nil
			}
			var lacking []string
			for r, n := range d.Lacking {
				lacking = append(lacking, fmt.Sprintf("%s (%d)", r, n))
			}
			sort.Strings(lacking)
			seen := "never"
			if d.FirstSeen != nil {
				seen = d.FirstSeen.Format("2006-01-02") + " to " + d.LastSeen.Format("2006-01-02")
			}
			ui.Fprint(w, fmt.Sprintf("\n%s %s, %d images, seen %s\n%s %s\n", ui.Dim.Render("kind:"), d.Kind, d.Images, seen,
				ui.Dim.Render("repos lacking it:"), strings.Join(lacking, ", ")))
			return nil
		}
		keys, err := svc.Labels(cmd.Context())
		if err != nil {
			return err
		}
		if out == render.JSON {
			return render.JSONTo(w, keys)
		}
		rows := make([][]any, len(keys))
		for i, k := range keys {
			rows[i] = []any{k.Key, k.Images, k.CoveragePct, k.Distinct, k.TopValues, k.Required}
		}
		return render.WriteRows(w, out, []string{"key", "images", "coverage_pct", "distinct", "top_values", "required"}, rows)
	},
}

// init registers the labels command and its flags.
func init() {
	labelsCmd.Flags().Bool("similar", false, "list near-duplicate keys")
	labelsCmd.Flags().StringP("output", "o", "table", "output format: table, json, csv or ndjson")
	rootCmd.AddCommand(labelsCmd)
}
