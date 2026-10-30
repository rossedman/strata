package cmd

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/mcpserver"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run an MCP server over stdio for AI agents",
	Long: `Run a Model Context Protocol server on stdin and stdout with tools for explain,
ancestry, descendants, query, stale and roots, so an agent can answer questions
such as "which prod images are on an EOL base?" from the index. strata serve
offers the same tools over HTTP at /mcp.`,
	Example: `  claude mcp add strata -- strata mcp
  strata mcp --db /var/lib/strata/strata.duckdb`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) (err error) {
		svc, err := state.Open(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Close(svc, &err)
		return mcpserver.New(svc, cli.Version).Run(cmd.Context(), &mcp.StdioTransport{})
	},
}

// init registers the mcp command and its flags.
func init() {
	rootCmd.AddCommand(mcpCmd)
}
