package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/query"
	"github.com/rossedman/strata/internal/render"
	"github.com/rossedman/strata/internal/ui"
)

var diffCmd = &cobra.Command{
	Use:   "diff",
	Short: "Report what changed in the registry since an earlier snapshot",
	Long: `Compare today's snapshot with the one from --since ago: new and removed repos, new
root bases, image count and deduplicated-bytes growth, shifts in the staleness mix,
changes in label coverage, and repos that went dark (no pushes in --dark-days while
still holding tagged images). Snapshots begin on the first index run.`,
	Example: `  strata diff --since 7d
  strata diff --since 30d --group-by label.team`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) (err error) {
		v := state.V
		if err := cli.OneOf("output", v.GetString("output"), render.Table, render.JSON); err != nil {
			return err
		}
		days, err := query.ParseDays(v.GetString("since"))
		if err != nil {
			return fmt.Errorf("--since: %w", err)
		}
		svc, err := state.Open(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Close(svc, &err)
		d, err := svc.Diff(cmd.Context(), time.Duration(days*24*float64(time.Hour)), v.GetString("group-by"), v.GetInt("dark-days"))
		if err != nil {
			return err
		}
		w := cli.Out(cmd)
		if v.GetString("output") == render.JSON {
			return render.JSONTo(w, d)
		}
		ui.Fprint(w, ui.Title.Render(fmt.Sprintf("Changes from %s to %s", d.From, d.To))+"\n")
		rows := make([][]any, len(d.Changes))
		for i, c := range d.Changes {
			rows[i] = []any{c.Kind, c.Group, c.Metric, c.From, c.To, c.To - c.From}
		}
		if err := render.WriteRows(w, render.Table, []string{"change", "group", "metric", "from", "to", "delta"}, rows); err != nil {
			return err
		}
		if len(d.DarkRepos) > 0 {
			ui.Fprint(w, "\n"+ui.Title.Render(fmt.Sprintf("Dark repos (no pushes in %d days)", v.GetInt("dark-days")))+"\n")
			for _, r := range d.DarkRepos {
				ui.Fprint(w, "  "+r+"\n")
			}
		}
		return nil
	},
}

// init registers the diff command and its flags.
func init() {
	f := diffCmd.Flags()
	f.String("since", "7d", "compare with the snapshot this long ago (such as 7d, 4w)")
	f.String("group-by", "", "compare per group: repo, root or a required label such as label.team")
	f.Int("dark-days", 90, "days without a push before a tagged repo counts as dark")
	f.StringP("output", "o", "table", "output format: table or json")
	rootCmd.AddCommand(diffCmd)
}
