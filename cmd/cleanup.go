package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/render"
	"github.com/rossedman/strata/internal/service"
	"github.com/rossedman/strata/internal/ui"
)

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "List images safe to delete and the storage deleting them frees",
	Long: `Lists deletion candidates: images the staleness policy calls abandoned
(untagged, nothing built on them, not pulled within abandoned_pull_days), or
those matching --where. A multi-platform index is a candidate only when every
platform in it is, and an image another kept image is built on is held.

reclaimable counts each layer once, and only when no kept image still uses it:
deleting two images that share a big layer frees it once, and deleting an image
whose layers a kept image shares frees nothing. exclusive is what deleting one
candidate alone frees. --scope repo counts a layer as freed when no kept image
in the same repo uses it, for registries that store layers per repository;
the default, registry, is the conservative count. Image configs are not
counted.

strata never deletes anything. Export the list with -o csv, or generate ECR
lifecycle policies with strata cleanup lifecycle.`,
	Example: `  strata cleanup
  strata cleanup --where 'stale = "abandoned" and repo ~ "*/sandbox/*"'
  strata cleanup --where 'tags = 0 and pushed_age > 180d and descendants = 0' --scope repo
  strata cleanup -o csv > candidates.csv`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) (err error) {
		v := state.V
		out := v.GetString("output")
		if err := cli.OneOf("output", out, render.Table, render.JSON, render.CSV, render.NDJSON); err != nil {
			return err
		}
		svc, release, err := state.Backend(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Release(release, &err)
		opts := service.CleanupOptions{Where: v.GetString("where"), Scope: v.GetString("scope")}
		if out == render.Table {
			opts.Limit = v.GetInt("limit")
		}
		c, err := svc.Cleanup(cmd.Context(), opts)
		if err != nil {
			return err
		}
		w := cli.Out(cmd)
		if out == render.JSON {
			return render.JSONTo(w, c)
		}
		rows := make([][]any, len(c.Candidates))
		for i, cc := range c.Candidates {
			rows[i] = cc.Row()
		}
		if out != render.Table {
			return render.WriteRows(w, out, service.CleanupColumns, rows)
		}
		ui.Fprint(w, fmt.Sprintf("%s %d candidates (%d images) matching %s: %s reclaimable of %s stated, %s scope\n",
			ui.Title.Render("cleanup"), c.Total, c.Images, c.Where, ui.Bytes(c.Reclaimable), ui.Bytes(c.Size), c.Scope))
		if c.Held > 0 {
			ui.Fprint(w, ui.Dim.Render(fmt.Sprintf("        %d matching images held: kept images are built on them or share their index\n", c.Held)))
		}
		if c.Total == 0 {
			return nil
		}
		repos := make([][]any, 0, len(c.Repos))
		for i, r := range c.Repos {
			if i == v.GetInt("limit") {
				break
			}
			repos = append(repos, []any{r.Repo, int64(r.Candidates), r.Size, r.Reclaimable})
		}
		ui.Fprint(w, "\n")
		if err := render.WriteRows(w, out, []string{"repo", "candidates", "size", "reclaimable_bytes"}, repos); err != nil {
			return err
		}
		ui.Fprint(w, "\n")
		for i := range rows {
			if r, ok := rows[i][8].(string); ok && len(r) > 60 {
				rows[i][8] = r[:59] + "…"
			}
		}
		if err := render.WriteRows(w, out, service.CleanupColumns, rows); err != nil {
			return err
		}
		if len(c.Candidates) < c.Total {
			ui.Fprint(w, ui.Dim.Render(fmt.Sprintf("\nlargest %d of %d listed; -o csv exports them all\n", len(c.Candidates), c.Total)))
		}
		return nil
	},
}

// init registers the cleanup command and its flags.
func init() {
	f := cleanupCmd.Flags()
	f.String("where", "", "select candidates with a query (default: "+service.DefaultCleanupFilter+")")
	f.String("scope", service.ScopeRegistry, "count a layer as freed when no kept image in the registry (registry) or in the same repo (repo) uses it")
	f.Int("limit", 20, "candidates and repos to list in table output; other formats list all")
	f.StringP("output", "o", "table", "output format: table, json, csv or ndjson")
	rootCmd.AddCommand(cleanupCmd)
}
