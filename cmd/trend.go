package cmd

import (
	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/render"
)

var trendCmd = &cobra.Command{
	Use:   "trend <metric>",
	Short: "Chart a snapshot metric over time",
	Long: `Render a daily snapshot metric over time as a self-contained HTML line chart, or
as rows. Metrics: repos, tags, manifests, unique_stacks, untagged_pct,
apparent_bytes, dedup_bytes, exclusive_bytes, shared_cost_bytes, fresh, drifting,
stale, abandoned.`,
	Example: `  strata trend dedup_bytes -o html > storage.html
  strata trend stale --group-by root -o csv`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		v := state.V
		out := v.GetString("output")
		if err := cli.OneOf("output", out, render.HTML, render.Table, render.JSON, render.CSV, render.NDJSON); err != nil {
			return err
		}
		svc, err := state.Open(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Close(svc, &err)
		points, err := svc.Trend(cmd.Context(), args[0], v.GetString("group-by"))
		if err != nil {
			return err
		}
		if out != render.HTML {
			rows := make([][]any, len(points))
			for i, p := range points {
				rows[i] = []any{p.Day, p.Group, p.Value}
			}
			return render.WriteRows(cli.Out(cmd), out, []string{"day", "group", args[0]}, rows)
		}
		var series []render.Series
		idx := map[string]int{}
		for _, p := range points {
			i, ok := idx[p.Group]
			if !ok {
				i = len(series)
				idx[p.Group] = i
				series = append(series, render.Series{Name: p.Group})
			}
			series[i].Days = append(series[i].Days, p.Day)
			series[i].Values = append(series[i].Values, p.Value)
		}
		return render.TrendTo(cli.Out(cmd), args[0], series)
	},
}

// init registers the trend command and its flags.
func init() {
	trendCmd.Flags().String("group-by", "", "one line per group: repo, root, or a required label such as label.team")
	trendCmd.Flags().StringP("output", "o", "html", "output format: html, table, json, csv or ndjson")
	rootCmd.AddCommand(trendCmd)
}
