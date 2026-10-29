package cmd

import "github.com/spf13/cobra"

var historyCmd = &cobra.Command{
	Use:   "history",
	Short: "Export or import tag history, the one part of the index that cannot be rebuilt",
	Long: `Registries only expose where a tag points now. strata records every tag move from
its first crawl on, which is what base-drift detection depends on. The rest of the
index can be rebuilt from the registry; tag history cannot, so keep a copy.`,
}

// init registers the history command and its flags.
func init() {
	rootCmd.AddCommand(historyCmd)
}
