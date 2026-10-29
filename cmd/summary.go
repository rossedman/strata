package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/render"
	"github.com/rossedman/strata/internal/service"
	"github.com/rossedman/strata/internal/ui"
)

var summaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "Show the shape of the registry in one screen",
	Long: `Count repos, tags and manifests, distinct layer stacks (how much is just retagging),
the untagged share, platforms, bytes and the staleness mix.

Apparent bytes sum every image's compressed layers as stated in its manifest.
Deduplicated bytes count each blob once, close to real storage. Grouped, exclusive
bytes are layers only that group uses (what removing it would free) and shared-cost
bytes split each shared layer equally among the groups using it (who uses most).`,
	Example: `  strata summary
  strata summary --group-by label.team
  strata summary --group-by root -o json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) (err error) {
		v := state.V
		if err := cli.OneOf("output", v.GetString("output"), render.Table, render.JSON, render.CSV); err != nil {
			return err
		}
		svc, release, err := state.Backend(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Release(release, &err)
		s, err := svc.Summary(cmd.Context(), v.GetString("group-by"))
		if err != nil {
			return err
		}
		out := cli.Out(cmd)
		switch v.GetString("output") {
		case render.JSON:
			return render.JSONTo(out, s)
		case render.CSV:
			return render.WriteRows(out, render.CSV, metricCols, metricRows(append([]service.GroupMetrics{s.Total}, s.Groups...)))
		}
		t := s.Total
		var b strings.Builder
		line := func(k, v string) { fmt.Fprintf(&b, "  %s %s\n", ui.Dim.Render(fmt.Sprintf("%-20s", k)), v) }
		b.WriteString(ui.Title.Render("Registry footprint") + "\n")
		line("repos", fmt.Sprint(t.Repos))
		line("tags", fmt.Sprint(t.Tags))
		line("manifests", fmt.Sprint(t.Manifests))
		line("unique layer stacks", fmt.Sprintf("%d (%.0f%% of manifests)", t.Stacks, pct(t.Stacks, t.Manifests)))
		line("untagged", fmt.Sprintf("%.1f%%", t.UntaggedPct))
		line("apparent bytes", ui.Bytes(t.ApparentBytes))
		line("deduplicated bytes", ui.Bytes(t.DedupBytes))
		line("staleness", fmt.Sprintf("%s %d  %s %d  %s %d  %s %d",
			ui.VerdictStyle("fresh").Render("fresh"), t.Fresh, ui.VerdictStyle("drifting").Render("drifting"), t.Drifting,
			ui.VerdictStyle("stale").Render("stale"), t.Stale, ui.VerdictStyle("abandoned").Render("abandoned"), t.Abandoned))
		plats := make([]string, 0, len(s.Platforms))
		for p, n := range s.Platforms {
			plats = append(plats, fmt.Sprintf("%s %d", p, n))
		}
		sort.Strings(plats)
		line("platforms", strings.Join(plats, ", "))
		if len(s.Compliance) > 0 {
			keys := make([]string, 0, len(s.Compliance))
			for k := range s.Compliance {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var parts []string
			for _, k := range keys {
				parts = append(parts, fmt.Sprintf("%s %.1f%%", k, s.Compliance[k]))
			}
			line("required labels", strings.Join(parts, ", "))
		}
		ui.Fprint(out, b.String())
		if len(s.Groups) > 0 {
			ui.Fprint(out, "\n")
			return render.WriteRows(out, render.Table, metricCols, metricRows(s.Groups))
		}
		return nil
	},
}

// metricCols are the columns of a grouped summary table.
var metricCols = []string{"group", "repos", "tags", "manifests", "unique_stacks", "untagged_pct", "apparent_bytes",
	"dedup_bytes", "exclusive_bytes", "shared_cost_bytes", "fresh", "drifting", "stale", "abandoned"}

// metricRows turns group metrics into table rows.
func metricRows(ms []service.GroupMetrics) [][]any {
	rows := make([][]any, len(ms))
	for i, m := range ms {
		rows[i] = []any{m.Group, m.Repos, m.Tags, m.Manifests, m.Stacks, m.UntaggedPct, m.ApparentBytes,
			m.DedupBytes, m.ExclusiveBytes, m.SharedBytes, m.Fresh, m.Drifting, m.Stale, m.Abandoned}
	}
	return rows
}

// pct returns a as a percentage of b.
func pct(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

// init registers the summary command and its flags.
func init() {
	summaryCmd.Flags().String("group-by", "", "group by repo, root or any field such as label.team")
	summaryCmd.Flags().StringP("output", "o", "table", "output format: table, json or csv")
	rootCmd.AddCommand(summaryCmd)
}
