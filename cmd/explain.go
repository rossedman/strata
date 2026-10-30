package cmd

import (
	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/render"
)

var explainCmd = &cobra.Command{
	Use:   "explain <ref>",
	Short: "Show what an image is, where it came from and whether it is stale",
	Long: `Show an image's labels, config, layers with the build step behind each, referrers,
its parent chain with the evidence for each edge, and its staleness verdict with
reasons. A ref is repo:tag, repo@digest, a digest, or a digest prefix; the registry
host can be left off when the rest is unambiguous.`,
	Example: `  strata explain reg.io/payments/api:1.8.2
  strata explain payments/api:1.8.2 --history
  strata explain sha256:9f2c41ab07e2 -o json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		v := state.V
		if err := cli.OneOf("output", v.GetString("output"), "text", render.JSON); err != nil {
			return err
		}
		svc, release, err := state.Backend(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Release(release, &err)
		ex, err := svc.Explain(cmd.Context(), args[0], v.GetString("platform"), v.GetBool("history"))
		if err != nil {
			return err
		}
		if v.GetString("output") == render.JSON {
			return render.JSONTo(cli.Out(cmd), ex)
		}
		return render.Explain(cli.Out(cmd), ex)
	},
}

// init registers the explain command and its flags.
func init() {
	explainCmd.Flags().Bool("history", false, "include build steps reconstructed from the config history")
	explainCmd.Flags().StringP("output", "o", "text", "output format: text or json")
	rootCmd.AddCommand(explainCmd)
}
