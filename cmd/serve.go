package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/api"
	"github.com/rossedman/strata/internal/cli"
	"github.com/rossedman/strata/internal/mcpserver"
	"github.com/rossedman/strata/internal/service"
	"github.com/rossedman/strata/internal/web"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve the web UI, the HTTP API and an MCP endpoint over the index",
	Long: `Serve the web UI at /, the JSON API under /v1 (spec at /openapi.json) and an MCP
server at /mcp from one process, which is the only one that opens the index for writing.

With --reindex-every it crawls the configured registries in the background: once
at startup, then on that schedule, so the index stays current without webhooks.
The UI is usable while the first crawl of a large registry runs: it answers from
what is indexed so far, shows the crawl's progress, and lineage, staleness and
the views refresh every --refresh-every until the crawl finishes. CLI commands
find a running server and route through it instead of opening the locked index
file.

Bound to loopback, the server needs no auth. On any other address it is shared
mode, and refuses to start without --auth-header: the name of a header a trusted
auth proxy sets (such as X-Forwarded-User). The lineage graph maps your attack
surface, so it is never served unauthenticated beyond localhost. The API is
read-only.`,
	Example: `  strata serve
  strata serve --reindex-every 6h
  strata serve --reindex-every 6h --refresh-every 5m
  strata serve --addr :8080 --auth-header X-Forwarded-User`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) (err error) {
		c := state.Config.Serve
		if err := checkShared(c.Addr, c.AuthHeader); err != nil {
			return err
		}
		svc, err := state.Open(cmd.Context())
		if err != nil {
			return err
		}
		defer cli.Close(svc, &err)
		svc.RefreshEvery = state.V.GetDuration("refresh-every")
		svc.Logger = state.Logger
		h, err := serveHandler(svc, c.AuthHeader)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithCancel(cmd.Context())
		// The re-crawl loop must stop before the deferred store close runs,
		// so cancel it and wait for it on the way out.
		var loop sync.WaitGroup
		defer loop.Wait()
		defer cancel()
		if every := state.V.GetDuration("reindex-every"); every > 0 {
			loop.Go(func() { reindexLoop(ctx, svc, every) })
		} else if need, err := svc.NeedsRefresh(ctx); err == nil && need {
			// An index run stopped before its refresh leaves lineage,
			// staleness and the views unbuilt; build them now, in the
			// background, so the UI fills in instead of staying empty.
			loop.Go(func() {
				state.Logger.Info("computing lineage and staleness for the existing index")
				if err := svc.Refresh(ctx); err != nil && ctx.Err() == nil {
					state.Logger.Error("refresh failed", "err", err)
				}
			})
		}
		srv := &http.Server{Addr: c.Addr, Handler: h, ReadHeaderTimeout: api.ReadHeaderTimeout}
		ln, err := net.Listen("tcp", c.Addr)
		if err != nil {
			return err
		}
		state.Logger.Info("serving", "ui", "http://"+ln.Addr().String()+"/", "api", "http://"+ln.Addr().String()+"/v1", "mcp", "http://"+ln.Addr().String()+"/mcp")
		go func() {
			<-ctx.Done()
			shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			_ = srv.Shutdown(shutdown)
		}()
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	},
}

// checkShared refuses a non-loopback address without auth.
func checkShared(addr, authHeader string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--addr %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
	if !loopback && authHeader == "" {
		return fmt.Errorf("refusing to serve on %s without auth: set --auth-header to the header your auth proxy sets, or bind to 127.0.0.1", addr)
	}
	return nil
}

// serveHandler mounts the API, the MCP endpoint and the spec.
func serveHandler(svc *service.Service, authHeader string) (http.Handler, error) {
	apiHandler, err := api.New(svc, api.Options{Version: cli.Version, AuthHeader: authHeader, Logger: state.Logger})
	if err != nil {
		return nil, err
	}
	mcpServer := mcpserver.New(svc, cli.Version)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, nil)
	spec, err := api.GetSpec()
	if err != nil {
		return nil, err
	}
	specJSON, err := spec.MarshalJSON()
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/", apiHandler)
	mux.Handle("/mcp", requireAuth(authHeader, mcpHandler))
	mux.Handle("/", requireAuth(authHeader, web.Handler()))
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(specJSON)
	})
	return mux, nil
}

// requireAuth applies the trusted-header check outside the API.
func requireAuth(header string, next http.Handler) http.Handler {
	if header == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(header) == "" {
			http.Error(w, "missing "+header+" header", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// reindexLoop crawls the configured registries now and then every
// interval, until ctx ends.
func reindexLoop(ctx context.Context, svc *service.Service, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		all, err := registryOptions(ctx, state.Config.Registries)
		if err != nil && ctx.Err() == nil {
			state.Logger.Error("index failed", "err", err)
		}
		for i, r := range state.Config.Registries {
			if ctx.Err() != nil || err != nil {
				break
			}
			opts := all[i]
			opts.External = state.Config.External
			res, ierr := svc.Index(ctx, opts)
			switch {
			case ierr == nil:
				state.Logger.Info("indexed", "registry", r.Host, "new_images", res.Crawl.Images, "tag_moves", res.Crawl.TagMoves)
			case ctx.Err() == nil:
				state.Logger.Error("index failed", "registry", r.Host, "err", ierr)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// init registers the serve command and its flags.
func init() {
	f := serveCmd.Flags()
	f.String("addr", "", "listen address (default: 127.0.0.1:7878)")
	f.String("auth-header", "", "header a trusted auth proxy sets on every request; required off loopback")
	f.Duration("reindex-every", 0, "crawl the configured registries at startup and then this often, such as 6h (0 disables)")
	f.Duration("refresh-every", 10*time.Minute, "while a crawl runs, refresh lineage, staleness and the views this often (0 waits for the crawl to finish)")
	rootCmd.AddCommand(serveCmd)
}
