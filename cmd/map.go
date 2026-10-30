package cmd

import (
	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/render"
)

var mapCmd = &cobra.Command{
	Use:   "map",
	Short: "Draw every root base and its sprawl as an icicle chart",
	Long: `Write the registry map: a self-contained HTML icicle chart with root bases at the
left and their descendants to the right. Each block is sized by image count and
shaded by its share of stale images, so base sprawl is visible at a glance. Click a
block to zoom into it.`,
	Example: `  strata map -o html > registry-map.html`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) (err error) {
		v := state.V
		if err := cli.OneOf("output", v.GetString("output"), render.HTML, render.JSON); err != nil {
			return err
		}
		svc, err := state.Open(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Close(svc, &err)
		g, err := svc.RootsGraph(cmd.Context())
		if err != nil {
			return err
		}
		if v.GetString("output") == render.JSON {
			return render.JSONTo(cli.Out(cmd), g)
		}
		return render.MapTo(cli.Out(cmd), g)
	},
}

// init registers the map command and its flags.
func init() {
	mapCmd.Flags().StringP("output", "o", "html", "output format: html or json")
	rootCmd.AddCommand(mapCmd)
}
