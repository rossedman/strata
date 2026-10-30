package cmd

import (
	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/render"
)

var basesSimilarCmd = &cobra.Command{
	Use:   "similar",
	Short: "Find near-duplicate bases that could be consolidated",
	Long: `Group bases (images with descendants) whose stacks match except for the last one or
two layers, with an estimate of the deduplicated bytes saved if each cluster were
standardized on one base.`,
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
		clusters, err := svc.SimilarBases(cmd.Context())
		if err != nil {
			return err
		}
		if v.GetString("output") == render.JSON {
			return render.JSONTo(cli.Out(cmd), clusters)
		}
		rows := make([][]any, len(clusters))
		for i, c := range clusters {
			rows[i] = []any{c.Bases, int64(c.SharedLayers), c.Descendants, c.SavedBytes}
		}
		return render.WriteRows(cli.Out(cmd), v.GetString("output"), []string{"bases", "shared_layers", "descendants", "saved_bytes"}, rows)
	},
}

// init registers the bases similar command and its flags.
func init() {
	basesSimilarCmd.Flags().StringP("output", "o", "table", "output format: table, json, csv or ndjson")
	basesCmd.AddCommand(basesSimilarCmd)
}
