package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/query"
)

var vulnsCmd = &cobra.Command{
	Use:   "vulns",
	Short: "Find which bases carry vulnerabilities and everything built on them",
	Long: `Vulnerability reports from the registry's own scans. ECR reports scans for
every image; strata index records counts by severity, and with --scan-findings
the vulnerabilities themselves, so impact can follow a CVE.

The same data is in the query language: vuln.critical, vuln.high, vuln.fixable,
vuln.id, vuln.package and vuln.scanned.`,
}

// inUseDays parses --in-use as whole days.
func inUseDays() (int, error) {
	d, err := query.ParseDays(state.V.GetString("in-use"))
	if err != nil {
		return 0, fmt.Errorf("--in-use: %w", err)
	}
	return max(int(d), 1), nil
}

// init registers the vulns command.
func init() {
	rootCmd.AddCommand(vulnsCmd)
}

// vulnFlags adds the flags vulns subcommands share.
func vulnFlags(c *cobra.Command) {
	f := c.Flags()
	f.String("group-by", "label.team", "field to group images by, such as label.team or repo")
	f.String("in-use", "30d", "an image pulled within this long counts as in use")
	f.StringP("output", "o", "table", "output format: table, json, csv or ndjson")
	vulnsCmd.AddCommand(c)
}
