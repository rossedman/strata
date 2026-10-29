package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rossedman/strata/internal/graph"
	"github.com/rossedman/strata/internal/service"
	"github.com/rossedman/strata/pkg/client"
)

// Backend answers read queries, from a local index or a running server.
// *service.Service satisfies it.
type Backend interface {
	Explain(ctx context.Context, ref, platform string, history bool) (*service.Explanation, error)
	Ancestry(ctx context.Context, ref, platform string) (*graph.Graph, error)
	Descendants(ctx context.Context, ref string, opts service.DescendantOptions) (*service.DescendantResult, error)
	Query(ctx context.Context, opts service.QueryOptions) (*service.Table, error)
	QueryGraph(ctx context.Context, filter string, budget int) (*graph.Graph, error)
	Stale(ctx context.Context, groupBy string) (*service.Table, error)
	Roots(ctx context.Context) ([]service.Root, error)
	Summary(ctx context.Context, groupBy string) (*service.Summary, error)
}

// Backend returns the local index, or a remote server when --server is
// set or the index is locked by a running strata serve. The returned
// close function releases it.
func (s *State) Backend(ctx context.Context) (Backend, func() error, error) {
	if url := s.Config.Serve.URL; url != "" {
		r, err := NewRemote(url, s.Config.Serve.AuthHeader)
		return r, func() error { return nil }, err
	}
	svc, err := s.Open(ctx)
	if err == nil {
		return svc, svc.Store.Close, nil
	}
	if !isLocked(err) {
		return nil, nil, err
	}
	// The index is held by another process; if it is strata serve on the
	// configured address, route through it.
	url := "http://" + s.Config.Serve.Addr
	r, rerr := NewRemote(url, s.Config.Serve.AuthHeader)
	if rerr == nil && r.Healthy(ctx) {
		s.Logger.Debug("index is locked; using the running server", "url", url)
		return r, func() error { return nil }, nil
	}
	return nil, nil, err
}

// Remote is a Backend over strata's HTTP API.
type Remote struct {
	c *client.ClientWithResponses
}

// NewRemote returns a client for a strata server. authHeader, if set, is
// sent with the current user name, for servers behind a trusted-header
// proxy in development.
func NewRemote(url, authHeader string) (*Remote, error) {
	opts := []client.ClientOption{client.WithHTTPClient(&http.Client{Timeout: 5 * time.Minute})}
	if authHeader != "" {
		opts = append(opts, client.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
			r.Header.Set(authHeader, "strata-cli")
			return nil
		}))
	}
	c, err := client.NewClientWithResponses(strings.TrimRight(url, "/"), opts...)
	if err != nil {
		return nil, fmt.Errorf("server %s: %w", url, err)
	}
	return &Remote{c: c}, nil
}

// Healthy reports whether the server answers its health check.
func (r *Remote) Healthy(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	res, err := r.c.GetHealthWithResponse(ctx)
	return err == nil && res.JSON200 != nil
}

// decode converts an API response body to a service type, or returns the
// server's error message.
func decode[T any](status int, body []byte, err error) (T, error) {
	var out T
	if err != nil {
		return out, err
	}
	if status != http.StatusOK {
		var e client.Error
		if json.Unmarshal(body, &e) == nil && e.Message != "" {
			if status == http.StatusNotFound {
				return out, fmt.Errorf("%s: %w", strings.TrimSuffix(e.Message, ": "+service.ErrNotFound.Error()), service.ErrNotFound)
			}
			return out, errors.New(e.Message)
		}
		return out, fmt.Errorf("server returned %d", status)
	}
	return out, json.Unmarshal(body, &out)
}

// ptr returns a pointer to v, or nil for the zero value.
func ptr[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

// Explain resolves the ref, then fetches the image.
func (r *Remote) Explain(ctx context.Context, ref, platform string, history bool) (*service.Explanation, error) {
	res, err := r.c.ResolveRefWithResponse(ctx, &client.ResolveRefParams{Ref: ref, Platform: ptr(platform)})
	if err != nil {
		return nil, err
	}
	resolved, err := decode[client.Resolved](res.StatusCode(), res.Body, nil)
	if err != nil {
		return nil, err
	}
	img, err := r.c.GetImageWithResponse(ctx, resolved.Digest, &client.GetImageParams{History: &history})
	if err != nil {
		return nil, err
	}
	return decode[*service.Explanation](img.StatusCode(), img.Body, nil)
}

// Ancestry fetches an ancestry graph.
func (r *Remote) Ancestry(ctx context.Context, ref, platform string) (*graph.Graph, error) {
	res, err := r.c.GetAncestryWithResponse(ctx, &client.GetAncestryParams{Ref: ref, Platform: ptr(platform)})
	if err != nil {
		return nil, err
	}
	return decode[*graph.Graph](res.StatusCode(), res.Body, nil)
}

// Descendants fetches a descendant graph. A versions file is local-only.
func (r *Remote) Descendants(ctx context.Context, ref string, opts service.DescendantOptions) (*service.DescendantResult, error) {
	if opts.VersionsFrom != "" {
		return nil, errors.New("--versions-from is not supported through a server; run against the index file")
	}
	depth := opts.Depth
	res, err := r.c.GetDescendantsWithResponse(ctx, &client.GetDescendantsParams{
		Ref: ref, Platform: ptr(opts.Platform), Depth: &depth, AllVersions: &opts.AllVersions, Budget: ptr(opts.Budget),
	})
	if err != nil {
		return nil, err
	}
	return decode[*service.DescendantResult](res.StatusCode(), res.Body, nil)
}

// Query runs a filter on the server.
func (r *Remote) Query(ctx context.Context, opts service.QueryOptions) (*service.Table, error) {
	p := &client.RunQueryParams{Filter: ptr(opts.Filter), GroupBy: ptr(opts.GroupBy), Limit: &opts.Limit}
	if len(opts.Fields) > 0 {
		p.Fields = &opts.Fields
	}
	if len(opts.Sort) > 0 {
		p.Sort = &opts.Sort
	}
	res, err := r.c.RunQueryWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	return decode[*service.Table](res.StatusCode(), res.Body, nil)
}

// QueryGraph draws a filter's matches on the server.
func (r *Remote) QueryGraph(ctx context.Context, filter string, budget int) (*graph.Graph, error) {
	res, err := r.c.GetQueryGraphWithResponse(ctx, &client.GetQueryGraphParams{Filter: ptr(filter), Budget: ptr(budget)})
	if err != nil {
		return nil, err
	}
	return decode[*graph.Graph](res.StatusCode(), res.Body, nil)
}

// Stale fetches the staleness report.
func (r *Remote) Stale(ctx context.Context, groupBy string) (*service.Table, error) {
	res, err := r.c.GetStaleWithResponse(ctx, &client.GetStaleParams{GroupBy: ptr(groupBy)})
	if err != nil {
		return nil, err
	}
	return decode[*service.Table](res.StatusCode(), res.Body, nil)
}

// Roots fetches the root bases.
func (r *Remote) Roots(ctx context.Context) ([]service.Root, error) {
	res, err := r.c.GetRootsWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	return decode[[]service.Root](res.StatusCode(), res.Body, nil)
}

// Summary fetches footprint metrics.
func (r *Remote) Summary(ctx context.Context, groupBy string) (*service.Summary, error) {
	res, err := r.c.GetSummaryWithResponse(ctx, &client.GetSummaryParams{GroupBy: ptr(groupBy)})
	if err != nil {
		return nil, err
	}
	return decode[*service.Summary](res.StatusCode(), res.Body, nil)
}

// Release calls a Backend close function, keeping the first error.
func Release(closeFn func() error, err *error) {
	if cerr := closeFn(); cerr != nil && *err == nil {
		*err = cerr
	}
}
