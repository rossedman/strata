package cmd

import (
	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/render"
	"github.com/rossedman/strata/internal/service"
)

var descendantsCmd = &cobra.Command{
	Use:   "descendants <ref>",
	Short: "Find everything built on a base, whatever it is named",
	Long: `Match every image whose layer stack starts with the base's, so descendants are
found even when their names and labels say nothing about the base.

A tag only points at today's digest. --all-versions widens the match to every
digest the tag has held since strata began recording (exact), plus older images
in the base repo created before recording began (inferred). --versions-from adds
digests or refs from a file, one per line (declared).`,
	Example: `  strata descendants reg.io/base/go:1.22
  strata descendants reg.io/base/go:1.22 --depth 0
  strata descendants reg.io/base/go:1.22 --all-versions --depth 0
  strata descendants reg.io/base/go:1.22 --all-versions --group-by base_version
  strata descendants reg.io/base/go:1.22 --depth 2 -o html > go-1.22.html`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		v := state.V
		groupBy := v.GetString("group-by")
		if groupBy != "" {
			if err := cli.OneOf("group-by", groupBy, "base_version"); err != nil {
				return err
			}
			if err := cli.OneOf("output", v.GetString("output"), render.FormatTree, render.Table, render.JSON, render.CSV, render.NDJSON); err != nil {
				return err
			}
		} else if err := cli.OneOf("output", v.GetString("output"), render.FormatTree, render.JSON, render.Mermaid, render.Dot, render.SVG, render.HTML); err != nil {
			return err
		}
		svc, release, err := state.Backend(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Release(release, &err)
		res, err := svc.Descendants(cmd.Context(), args[0], service.DescendantOptions{
			Platform:     v.GetString("platform"),
			Depth:        v.GetInt("depth"),
			AllVersions:  v.GetBool("all-versions"),
			VersionsFrom: v.GetString("versions-from"),
			Budget:       v.GetInt("budget"),
		})
		if err != nil {
			return err
		}
		out := v.GetString("output")
		if groupBy != "" {
			if out == render.FormatTree {
				out = render.Table
			}
			cols := []string{"base_version", "digest", "source", "confidence", "current", "children", "descendants"}
			rows := make([][]any, len(res.Versions))
			for i, b := range res.Versions {
				rows[i] = []any{b.Label, b.Digest, b.Source, b.Confidence, b.Current, int64(b.Children), int64(b.Descendants)}
			}
			return render.WriteRows(cli.Out(cmd), out, cols, rows)
		}
		if out == render.JSON {
			return render.JSONTo(cli.Out(cmd), res)
		}
		return render.WriteGraph(cli.Out(cmd), out, res.Graph)
	},
}

// init registers the descendants command and its flags.
func init() {
	f := descendantsCmd.Flags()
	f.Int("depth", 1, "levels below the base to include; 0 means the whole subtree")
	f.Bool("all-versions", false, "match every digest the base tag has pointed to")
	f.String("versions-from", "", "file of extra base digests or refs, one per line")
	f.String("group-by", "", "summarize instead of drawing: base_version counts images per base version")
	f.Int("budget", 500, "maximum graph nodes; siblings in one repo collapse first")
	f.StringP("output", "o", "tree", "output format: tree, json, mermaid, dot, svg or html (table, csv or ndjson with --group-by)")
	rootCmd.AddCommand(descendantsCmd)
}
