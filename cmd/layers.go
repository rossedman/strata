package cmd

import "github.com/spf13/cobra"

var layersCmd = &cobra.Command{
	Use:   "layers",
	Short: "Analyze layers: what is most shared and where the bytes go",
	Long: `Layer analytics. Each non-empty history entry maps to one layer, so every layer
is traced to the build command that created it.`,
}

// init registers the layers command and its flags.
func init() {
	rootCmd.AddCommand(layersCmd)
}
