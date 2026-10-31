package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/render"
	"github.com/rossedman/strata/internal/service"
	"github.com/rossedman/strata/internal/ui"
)

var vulnsImpactCmd = &cobra.Command{
	Use:   "impact <base-ref | vulnerability-id>",
	Short: "Show everything a vulnerable base or a CVE reaches, grouped by team",
	Long: `Given a base, lists every image built on it. Given a vulnerability id such as
CVE-2024-3094 (which needs findings from strata index --scan-findings), lists
every image whose scan found it, plus unscanned images built on one of those,
and the images it enters through. A scan without the vulnerability stops the
walk: that image was patched.

The table output totals images per team (label.team, or --group-by); --images
lists them. csv and ndjson export the images, for a ticket or a spreadsheet.
--where narrows the images with the query language: use
'stale.last_pulled < 30d' to keep only images in use.`,
	Example: `  strata vulns impact reg.io/base/debian:bookworm
  strata vulns impact CVE-2024-3094 --where 'stale.last_pulled < 30d'
  strata vulns impact CVE-2024-3094 --images
  strata vulns impact CVE-2024-3094 -o csv > cve-2024-3094.csv`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		v := state.V
		out := v.GetString("output")
		if err := cli.OneOf("output", out, render.Table, render.JSON, render.CSV, render.NDJSON); err != nil {
			return err
		}
		inUse, err := inUseDays()
		if err != nil {
			return err
		}
		opts := service.ImpactOptions{Filter: v.GetString("where"), GroupBy: v.GetString("group-by"), InUseDays: inUse}
		if service.IsVulnID(args[0]) {
			opts.CVE = args[0]
		} else {
			opts.Ref = args[0]
		}
		svc, release, err := state.Backend(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Release(release, &err)
		im, err := svc.Impact(cmd.Context(), opts)
		if err != nil {
			return err
		}
		w := cli.Out(cmd)
		if out == render.JSON {
			return render.JSONTo(w, im)
		}
		if out != render.Table || v.GetBool("images") {
			rows := make([][]any, len(im.Images))
			for i, img := range im.Images {
				rows[i] = img.Row()
			}
			return render.WriteRows(w, out, service.ImpactColumns, rows)
		}
		ui.Fprint(w, fmt.Sprintf("%s %s reaches %d images, %d in use\n\n", ui.Title.Render("impact"), im.Target, im.Total, im.InUse))
		if len(im.Images) == 0 {
			return nil
		}
		rows := make([][]any, len(im.Groups))
		for i, g := range im.Groups {
			val := g.Value
			if val == "" {
				val = "(none)"
			}
			rows[i] = []any{val, int64(g.Images), int64(g.InUse), int64(g.Repos)}
		}
		if err := render.WriteRows(w, out, []string{im.GroupBy, "images", "in_use", "repos"}, rows); err != nil {
			return err
		}
		rows = make([][]any, len(im.Origins))
		for i, o := range im.Origins {
			rows[i] = []any{o.Ref, int64(o.Images), o.Digest}
		}
		ui.Fprint(w, "\n"+ui.Title.Render("enters through")+"\n")
		if err := render.WriteRows(w, out, []string{"image", "affected", "digest"}, rows); err != nil {
			return err
		}
		ui.Fprint(w, ui.Dim.Render("\n--images lists every image; -o csv exports them\n"))
		return nil
	},
}

// init registers the vulns impact command and its flags.
func init() {
	vulnsImpactCmd.Flags().String("where", "", "narrow the images with a query, such as 'stale.last_pulled < 30d'")
	vulnsImpactCmd.Flags().Bool("images", false, "list every image instead of totals per group")
	vulnFlags(vulnsImpactCmd)
}
