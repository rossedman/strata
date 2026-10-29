package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/query"
	"github.com/rossedman/strata/internal/render"
	"github.com/rossedman/strata/internal/service"
)

var queryCmd = &cobra.Command{
	Use:   "query <filter>",
	Short: "Filter images with strata's query language",
	Long: `Filter images with a small language that compiles to SQL, so every filter runs
inside DuckDB. Combine predicates with and, or, not and parentheses.

Operators: =  !=  <  <=  >  >=  ~ (glob)  in [...]  exists
A field on its own is a condition: has:sbom, stale, label.deprecated.
Functions: ancestor("<ref>") is true if the ref is anywhere in the ancestry.

Fields:
` + fieldHelp(),
	Example: `  strata query 'label.team = "payments" and age > 180d'
  strata query 'ancestor("reg.io/base/go:1.21") and not has:sbom'
  strata query 'stale in ["stale", "abandoned"] and label.env = "prod"'
  strata query 'tags = 0 and descendants = 0' -o ndjson > orphans.ndjson
  strata query 'missing:team' --group-by repo
  strata query 'label.team = "payments" and stale = "stale"' --graph -o html > stale.html`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		v := state.V
		out := v.GetString("output")
		if v.GetBool("graph") {
			if out == render.Table {
				out = render.FormatTree
			}
			if err := cli.OneOf("output", out, render.FormatTree, render.JSON, render.Mermaid, render.Dot, render.SVG, render.HTML); err != nil {
				return err
			}
		} else if err := cli.OneOf("output", out, render.Table, render.JSON, render.CSV, render.NDJSON); err != nil {
			return err
		}
		svc, release, err := state.Backend(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Release(release, &err)
		if v.GetBool("graph") {
			g, err := svc.QueryGraph(cmd.Context(), args[0], v.GetInt("budget"))
			if err != nil {
				return err
			}
			return render.WriteGraph(cli.Out(cmd), out, g)
		}
		opts := service.QueryOptions{
			Filter:  args[0],
			Fields:  splitList(v.GetStringSlice("fields")),
			Sort:    splitList(v.GetStringSlice("sort")),
			Limit:   v.GetInt("limit"),
			GroupBy: v.GetString("group-by"),
		}
		local, ok := svc.(*service.Service)
		if !ok {
			t, err := svc.Query(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return render.WriteRows(cli.Out(cmd), out, t.Columns, t.Rows)
		}
		// Locally, rows stream straight from DuckDB, so a full export
		// never buffers.
		rw, err := render.NewRowWriter(cli.Out(cmd), out)
		if err != nil {
			return err
		}
		if err := local.QueryEach(cmd.Context(), opts, rw.Header, rw.Row); err != nil {
			return err
		}
		return rw.Close()
	},
}

// splitList flattens comma-separated values.
func splitList(in []string) []string {
	var out []string
	for _, s := range in {
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// fieldHelp lists the query fields for --help.
func fieldHelp() string {
	fields := query.Fields()
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	var b strings.Builder
	for _, f := range fields {
		fmt.Fprintf(&b, "  %-22s %s\n", f.Name, f.Doc)
	}
	return b.String()
}

// init registers the query command and its flags.
func init() {
	f := queryCmd.Flags()
	f.StringSlice("fields", nil, "columns to return (default: "+strings.Join(service.DefaultFields, ",")+")")
	f.StringSlice("sort", nil, "fields to sort by; prefix with - for descending, such as -age")
	f.Int("limit", 0, "maximum rows (0 means all)")
	f.String("group-by", "", "count images per value of a field instead of listing them")
	f.Bool("graph", false, "draw the matches and their ancestry up to shared roots")
	f.Int("budget", 500, "maximum graph nodes with --graph")
	f.StringP("output", "o", "table", "output format: table, json, csv or ndjson (tree, mermaid, dot, svg or html with --graph)")
	rootCmd.AddCommand(queryCmd)
}
