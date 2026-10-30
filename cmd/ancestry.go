package cmd

import (
	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/render"
)

var ancestryCmd = &cobra.Command{
	Use:   "ancestry <ref>",
	Short: "Walk an image's ancestry back to its root base",
	Long: `Print the chain from an image to its root base. Each edge is labeled with how the
parent was found: layers (exact prefix match), annotation or label (declared).
A declared parent that disagrees with the layers is shown as a conflict.`,
	Example: `  strata ancestry reg.io/payments/api:1.8.2
  strata ancestry reg.io/payments/api@sha256:9f2c... -o svg > chain.svg
  strata ancestry payments/api:1.8.2 -o mermaid`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		v := state.V
		if err := cli.OneOf("output", v.GetString("output"), render.FormatTree, render.JSON, render.Mermaid, render.Dot, render.SVG, render.HTML); err != nil {
			return err
		}
		svc, release, err := state.Backend(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Release(release, &err)
		g, err := svc.Ancestry(cmd.Context(), args[0], v.GetString("platform"))
		if err != nil {
			return err
		}
		return render.WriteGraph(cli.Out(cmd), v.GetString("output"), g)
	},
}

// init registers the ancestry command and its flags.
func init() {
	ancestryCmd.Flags().StringP("output", "o", "tree", "output format: tree, json, mermaid, dot, svg or html")
	rootCmd.AddCommand(ancestryCmd)
}
