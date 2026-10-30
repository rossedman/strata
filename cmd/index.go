package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/config"
	"github.com/rossedman/strata/internal/crawl"
	"github.com/rossedman/strata/internal/ecr"
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

Registries come from --registry or from the registries list in the config file.
Filter flags given without --registry replace those filters on every configured
registry for this run.

Filters skip work before it is requested: --include-repo and --exclude-repo
before a repo's tags are listed, --include-tag and --exclude-tag before a tag is
resolved, and --platforms before a platform's manifest is read. Repos and tags a
filter skips are left as the index last saw them.

ECR registries are listed through the ECR API, with credentials from the AWS
credential chain. That needs no request per tag, records push and last-pull
times, and allows --pushed-since.`,
	Example: `  strata index --registry reg.io --workers 64
  strata index --registry localhost:5000 --insecure --repo payments/api
  strata index --registry reg.io --include-repo 'team-*/**' --exclude-tag 'pr-*' --platforms linux/amd64
  strata index --registry 123456789012.dkr.ecr.us-east-1.amazonaws.com --pushed-since 90d
  strata index --external docker.io/library/debian:bookworm-slim`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) (err error) {
		v := state.V
		regs := state.Config.Registries
		if host := v.GetString("registry"); host != "" {
			r := config.Registry{Host: host, Repos: v.GetStringSlice("repo"), Insecure: v.GetBool("insecure"),
				Workers: v.GetInt("workers"), Rate: v.GetFloat64("rate"), Provider: v.GetString("provider"),
				IncludeRepos: v.GetStringSlice("include-repo"), ExcludeRepos: v.GetStringSlice("exclude-repo"),
				IncludeTags: v.GetStringSlice("include-tag"), ExcludeTags: v.GetStringSlice("exclude-tag"),
				Platforms: v.GetStringSlice("platforms"), PushedSince: v.GetString("pushed-since")}
			if err := r.Check(); err != nil {
				return err
			}
			regs = []config.Registry{r}
		} else {
			// Filter flags narrow the configured registries for this run.
			regs = append([]config.Registry{}, regs...)
			f := cmd.Flags()
			for i := range regs {
				r := &regs[i]
				for flag, field := range map[string]*[]string{
					"include-repo": &r.IncludeRepos, "exclude-repo": &r.ExcludeRepos,
					"include-tag": &r.IncludeTags, "exclude-tag": &r.ExcludeTags, "platforms": &r.Platforms,
				} {
					if f.Changed(flag) {
						*field = v.GetStringSlice(flag)
					}
				}
				if f.Changed("pushed-since") {
					r.PushedSince = v.GetString("pushed-since")
				}
				if err := r.Check(); err != nil {
					return fmt.Errorf("%s: %w", r.Host, err)
				}
			}
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
			opts, err := crawlOptions(cmd.Context(), r)
			if err != nil {
				return err
			}
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
			opts, err := crawlOptions(cmd.Context(), config.Registry{})
			if err != nil {
				return err
			}
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
			ui.Fprint(cli.Out(cmd), fmt.Sprintf("%s %d repos (%d resumed), %d tags, %d new images, %d tag moves, %d requests in %s\n",
				ui.Title.Render("indexed"), c.Repos, c.Skipped, c.Tags, c.Images, c.TagMoves, c.Requests, r.Duration.Round(time.Millisecond)))
			if c.Images > 0 {
				ui.Fprint(cli.Out(cmd), fmt.Sprintf("%s %.1f requests per new image\n", ui.Dim.Render("      "), c.RequestsPerImage()))
			}
			if c.ReposFiltered+c.TagsFiltered+c.ImagesFiltered > 0 {
				ui.Fprint(cli.Out(cmd), fmt.Sprintf("%s filtered out %d repos, %d tags, %d images\n",
					ui.Dim.Render("      "), c.ReposFiltered, c.TagsFiltered, c.ImagesFiltered))
			}
			ui.Fprint(cli.Out(cmd), fmt.Sprintf("%s %d images indexed\n", ui.Dim.Render("      "), r.Images))
		}
		return nil
	},
}

// newECR opens an ECR registry client; tests replace it.
var newECR = func(ctx context.Context, host string) (*ecr.Registry, error) { return ecr.New(ctx, host) }

// crawlOptions builds crawl options from a registry entry and the config.
func crawlOptions(ctx context.Context, r config.Registry) (crawl.Options, error) {
	v := state.V
	filter, err := r.Filter()
	if err != nil {
		return crawl.Options{}, err
	}
	opts := crawl.Options{
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
		Filter:            filter,
	}
	if r.Host != "" && r.UsesECR() {
		reg, err := newECR(ctx, r.Host)
		if err != nil {
			return crawl.Options{}, err
		}
		opts.Lister = reg
		opts.Keychain = authn.NewMultiKeychain(reg, authn.DefaultKeychain)
	}
	return opts, nil
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
	f.String("provider", "auto", "how to list the registry: auto, ecr or distribution")
	f.StringSlice("include-repo", nil, "crawl only repos matching this glob (repeatable; ** crosses path segments)")
	f.StringSlice("exclude-repo", nil, "skip repos matching this glob (repeatable)")
	f.StringSlice("include-tag", nil, "crawl only tags matching this glob (repeatable)")
	f.StringSlice("exclude-tag", nil, "skip tags matching this glob, such as pr-* (repeatable)")
	f.StringSlice("platforms", nil, "keep only these platforms of multi-platform images, such as linux/amd64 (repeatable)")
	f.String("pushed-since", "", "skip images pushed longer ago than this, such as 90d (ECR only)")
	f.StringP("output", "o", "text", "output format: text or json")
	rootCmd.AddCommand(indexCmd)
}
