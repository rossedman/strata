package cmd

import (
	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/render"
	"github.com/rossedman/strata/internal/service"
)

var layersTopCmd = &cobra.Command{
	Use:   "top",
	Short: "List the most widely shared or the biggest layers",
	Long: `--by shares shows the real foundations of the registry, which are often different
from the bases people name. --by size shows the biggest layers overall.`,
	Example: `  strata layers top --by shares
  strata layers top --by size --limit 50`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) (err error) {
		v := state.V
		if err := cli.OneOf("output", v.GetString("output"), render.Table, render.JSON, render.CSV, render.NDJSON); err != nil {
			return err
		}
		svc, err := state.Open(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Close(svc, &err)
		layers, err := svc.TopLayers(cmd.Context(), v.GetString("by"), v.GetInt("limit"))
		if err != nil {
			return err
		}
		return writeLayers(cmd, layers)
	},
}

// writeLayers prints layer stats in the chosen format.
func writeLayers(cmd *cobra.Command, layers []service.LayerStat) error {
	out := state.V.GetString("output")
	if out == render.JSON {
		return render.JSONTo(cli.Out(cmd), layers)
	}
	rows := make([][]any, len(layers))
	for i, l := range layers {
		created := l.CreatedBy
		if out == render.Table && len(created) > 60 {
			created = created[:59] + "…"
		}
		rows[i] = []any{l.Blob, l.Size, l.Images, l.Repos, l.Example, created}
	}
	return render.WriteRows(cli.Out(cmd), out, []string{"blob_digest", "size", "images", "repos", "example_repo", "created_by"}, rows)
}

// init registers the layers top command and its flags.
func init() {
	layersTopCmd.Flags().String("by", "shares", "rank by shares or size")
	layersTopCmd.Flags().Int("limit", 20, "number of layers")
	layersTopCmd.Flags().StringP("output", "o", "table", "output format: table, json, csv or ndjson")
	layersCmd.AddCommand(layersTopCmd)
}
