package cmd

import (
	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/query"
	"github.com/rossedman/strata/internal/render"
)

var layersBloatCmd = &cobra.Command{
	Use:   "bloat",
	Short: "List big layers that nothing else reuses, with the command behind them",
	Example: `  strata layers bloat
  strata layers bloat --group-by repo --min-size 500MB`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) (err error) {
		v := state.V
		if err := cli.OneOf("output", v.GetString("output"), render.Table, render.JSON, render.CSV, render.NDJSON); err != nil {
			return err
		}
		perRepo := false
		if g := v.GetString("group-by"); g != "" {
			if err := cli.OneOf("group-by", g, "repo"); err != nil {
				return err
			}
			perRepo = true
		}
		minSize, err := query.ParseBytes(v.GetString("min-size"))
		if err != nil {
			return err
		}
		svc, err := state.Open(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Close(svc, &err)
		layers, err := svc.Bloat(cmd.Context(), perRepo, minSize, v.GetInt("limit"))
		if err != nil {
			return err
		}
		return writeLayers(cmd, layers)
	},
}

// init registers the layers bloat command and its flags.
func init() {
	f := layersBloatCmd.Flags()
	f.String("group-by", "", "repo: layers used by one repo only (default: by one image only)")
	f.String("min-size", "100MB", "smallest layer to report")
	f.Int("limit", 20, "number of layers")
	f.StringP("output", "o", "table", "output format: table, json, csv or ndjson")
	layersCmd.AddCommand(layersBloatCmd)
}
