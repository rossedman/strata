package cmd

import "github.com/spf13/cobra"

var basesCmd = &cobra.Command{
	Use:   "bases",
	Short: "Analyze base images",
}

// init registers the bases command and its flags.
func init() {
	rootCmd.AddCommand(basesCmd)
}
