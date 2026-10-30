package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/config"
	"github.com/rossedman/strata/internal/crawl"
	"github.com/rossedman/strata/internal/render"
	"github.com/rossedman/strata/internal/service"
	"github.com/rossedman/strata/internal/ui"
)

var indexCmd = &cobra.Command{
	Use:   "index",
	Short: "Crawl a registry and build or update the index",
	Long: `Crawl repos, tags, manifests and image configs, then recompute lineage, staleness
and today's snapshot. The crawl is resumable: if it stops, the next run skips the
repos it already finished. Re-crawls only list tags and compare digests, and every
tag move is recorded in tag history from the first crawl on.

Registries come from --registry or from the registries list in the config file.`,
	Example: `  strata index --registry reg.io --workers 64
  strata index --registry localhost:5000 --insecure --repo payments/api
  strata index --external docker.io/library/debian:bookworm-slim`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) (err error) {
		v := state.V
		regs := state.Config.Registries
		if host := v.GetString("registry"); host != "" {
			regs = []config.Registry{{Host: host, Repos: v.GetStringSlice("repo"), Insecure: v.GetBool("insecure"),
				Workers: v.GetInt("workers"), Rate: v.GetFloat64("rate")}}
		}
		external := append(append([]string{}, state.Config.External...), v.GetStringSlice("external")...)
		if len(regs) == 0 && len(external) == 0 {
			return fmt.Errorf("nothing to index: pass --registry or list registries in the config file")
		}
		svc, err := state.Open(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Close(svc, &err)
		var results []service.IndexResult
		for i, r := range regs {
			opts := crawlOptions(r)
			if i == 0 {
				opts.External = external
			}
			res, err := runIndex(cmd, svc, opts, "Indexing "+r.Host+"…")
			if err != nil {
				return err
			}
			results = append(results, res)
		}
		if len(regs) == 0 {
			opts := crawlOptions(config.Registry{})
			opts.External = external
			res, err := runIndex(cmd, svc, opts, "Fetching external bases…")
			if err != nil {
				return err
			}
			results = append(results, res)
		}
		if v.GetString("output") == render.JSON {
			return render.JSONTo(cli.Out(cmd), results)
		}
		for _, r := range results {
			c := r.Crawl
			ui.Fprint(cli.Out(cmd), fmt.Sprintf("%s %d repos (%d resumed), %d tags, %d new images, %d tag moves, %d requests in %s\n%s %d images indexed\n",
				ui.Title.Render("indexed"), c.Repos, c.Skipped, c.Tags, c.Images, c.TagMoves, c.Requests, r.Duration.Round(time.Millisecond),
				ui.Dim.Render("      "), r.Images))
		}
		return nil
	},
}

// crawlOptions builds crawl options from a registry entry and the config.
func crawlOptions(r config.Registry) crawl.Options {
	v := state.V
	return crawl.Options{
		Registry:          r.Host,
		Repos:             r.Repos,
		Workers:           r.Workers,
		RequestsPerSecond: r.Rate,
		Insecure:          r.Insecure,
		Referrers:         v.GetBool("referrers"),
		EnvAllowlist:      state.Config.EnvAllowlist,
		Redactions:        append(append([]string{}, crawl.DefaultRedactions...), state.Config.Redactions...),
		UserAgent:         "strata/" + cli.Version,
		Logger:            state.Logger,
	}
}

// runIndex runs one index pass under a spinner.
func runIndex(cmd *cobra.Command, svc *service.Service, opts crawl.Options, title string) (service.IndexResult, error) {
	var res service.IndexResult
	err := cli.Spin(cmd.Context(), cmd, title, func(ctx context.Context) error {
		var err error
		res, err = svc.Index(ctx, opts)
		return err
	})
	return res, err
}

// init registers the index command and its flags.
func init() {
	f := indexCmd.Flags()
	f.String("registry", "", "registry host to crawl, such as reg.io (overrides the config's registries)")
	f.StringSlice("repo", nil, "crawl only these repos (repeatable); default is the registry catalog")
	f.Int("workers", 16, "concurrent requests")
	f.Float64("rate", 0, "request limit per second (0 means unlimited)")
	f.Bool("insecure", false, "allow plain HTTP")
	f.StringSlice("external", nil, "external base refs to fetch so roots resolve (repeatable)")
	f.Bool("referrers", true, "fetch SBOMs, signatures and attestations")
	f.StringP("output", "o", "text", "output format: text or json")
	rootCmd.AddCommand(indexCmd)
}
