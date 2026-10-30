package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	middleware "github.com/oapi-codegen/nethttp-middleware"

	"github.com/rossedman/strata/internal/graph"
	"github.com/rossedman/strata/internal/service"
)

// Server implements the generated strict server interface over a service.
type Server struct {
	svc     *service.Service
	version string
}

var _ StrictServerInterface = (*Server)(nil)

// Options configure the HTTP handler.
type Options struct {
	// Version is reported by /v1/healthz.
	Version string
	// AuthHeader, if set, is a header a trusted auth proxy sets on every
	// request; requests without it are rejected.
	AuthHeader string
	// Logger records failed requests.
	Logger *slog.Logger
}

// New returns the API as an http.Handler: requests are validated
// against the OpenAPI spec before any handler runs.
func New(svc *service.Service, opts Options) (http.Handler, error) {
	spec, err := GetSpec()
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI spec: %w", err)
	}
	spec.Servers = nil // validate paths regardless of the host serving them
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	strict := NewStrictHandlerWithOptions(&Server{svc: svc, version: opts.Version}, nil, StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadRequest, err)
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error("request failed", "path", r.URL.Path, "err", err)
			writeError(w, http.StatusInternalServerError, err)
		},
	})
	mux := http.NewServeMux()
	h := HandlerFromMux(strict, mux)
	h = middleware.OapiRequestValidatorWithOptions(spec, &middleware.Options{
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, _ *http.Request, o middleware.ErrorHandlerOpts) {
			writeError(w, o.StatusCode, err)
		},
	})(h)
	return requireHeader(opts.AuthHeader, h), nil
}

// requireHeader rejects requests that lack the trusted auth header.
func requireHeader(header string, next http.Handler) http.Handler {
	if header == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(header) == "" {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("missing %s header; requests must come through the auth proxy", header))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeError writes an Error body.
func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Error{Message: err.Error()})
}

// convert maps a service value onto its generated API type. The two share
// a JSON shape by design; tests validate responses against the spec.
func convert[T any](src any) (T, error) {
	var dst T
	b, err := json.Marshal(src)
	if err != nil {
		return dst, err
	}
	err = json.Unmarshal(b, &dst)
	return dst, err
}

// classify sorts an error into not found, bad input or a failure.
func classify(err error) (status int, body Error) {
	body = Error{Message: err.Error()}
	switch {
	case errors.Is(err, service.ErrNotFound):
		return http.StatusNotFound, body
	case service.IsInputError(err):
		return http.StatusBadRequest, body
	}
	return http.StatusInternalServerError, body
}

// str dereferences an optional string.
func str[T ~string](p *T) string {
	if p == nil {
		return ""
	}
	return string(*p)
}

// num dereferences an optional int with a default.
func num(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// GetHealth reports the server version and index size.
func (s *Server) GetHealth(ctx context.Context, _ GetHealthRequestObject) (GetHealthResponseObject, error) {
	var n int64
	if err := s.svc.Store.DB().QueryRowContext(ctx, `SELECT count(*) FROM images`).Scan(&n); err != nil {
		st, body := classify(err)
		return GetHealthdefaultJSONResponse{Body: body, StatusCode: st}, nil
	}
	return GetHealth200JSONResponse{Status: Ok, Version: s.version, Images: n}, nil
}

// ResolveRef resolves a ref to a digest.
func (s *Server) ResolveRef(ctx context.Context, r ResolveRefRequestObject) (ResolveRefResponseObject, error) {
	d, err := s.svc.Resolve(ctx, r.Params.Ref, str(r.Params.Platform))
	if err != nil {
		st, body := classify(err)
		if st == http.StatusNotFound {
			return ResolveRef404JSONResponse{NotFoundJSONResponse(body)}, nil
		}
		return ResolveRefdefaultJSONResponse{Body: body, StatusCode: st}, nil
	}
	return ResolveRef200JSONResponse{Ref: r.Params.Ref, Digest: d}, nil
}

// GetImage explains one image.
func (s *Server) GetImage(ctx context.Context, r GetImageRequestObject) (GetImageResponseObject, error) {
	history := r.Params.History != nil && *r.Params.History
	ex, err := s.svc.Explain(ctx, r.Digest, "", history)
	if err == nil {
		var out Explanation
		if out, err = convert[Explanation](ex); err == nil {
			return GetImage200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	if st == http.StatusNotFound {
		return GetImage404JSONResponse{NotFoundJSONResponse(body)}, nil
	}
	return GetImagedefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// GetAncestry walks an image's ancestry.
func (s *Server) GetAncestry(ctx context.Context, r GetAncestryRequestObject) (GetAncestryResponseObject, error) {
	g, err := s.svc.Ancestry(ctx, r.Params.Ref, str(r.Params.Platform))
	if err == nil {
		var out Graph
		if out, err = convert[Graph](g); err == nil {
			return GetAncestry200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	if st == http.StatusNotFound {
		return GetAncestry404JSONResponse{NotFoundJSONResponse(body)}, nil
	}
	return GetAncestrydefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// GetDescendants finds everything built on a base.
func (s *Server) GetDescendants(ctx context.Context, r GetDescendantsRequestObject) (GetDescendantsResponseObject, error) {
	p := r.Params
	res, err := s.svc.Descendants(ctx, p.Ref, service.DescendantOptions{
		Platform:    str(p.Platform),
		Depth:       num(p.Depth, 1),
		AllVersions: p.AllVersions != nil && *p.AllVersions,
		Budget:      num(p.Budget, graph.DefaultBudget),
	})
	if err == nil {
		var out DescendantResult
		if out, err = convert[DescendantResult](res); err == nil {
			return GetDescendants200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	if st == http.StatusNotFound {
		return GetDescendants404JSONResponse{NotFoundJSONResponse(body)}, nil
	}
	return GetDescendantsdefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// RunQuery filters images.
func (s *Server) RunQuery(ctx context.Context, r RunQueryRequestObject) (RunQueryResponseObject, error) {
	p := r.Params
	opts := service.QueryOptions{Filter: str(p.Filter), Limit: num(p.Limit, 1000), GroupBy: str(p.GroupBy)}
	if p.Fields != nil {
		opts.Fields = *p.Fields
	}
	if p.Sort != nil {
		opts.Sort = *p.Sort
	}
	t, err := s.svc.Query(ctx, opts)
	if err == nil {
		var out Table
		if out, err = convert[Table](t); err == nil {
			return RunQuery200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	if st == http.StatusBadRequest {
		return RunQuery400JSONResponse{BadRequestJSONResponse(body)}, nil
	}
	return RunQuerydefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// GetQueryGraph draws a filter's matches and their ancestry.
func (s *Server) GetQueryGraph(ctx context.Context, r GetQueryGraphRequestObject) (GetQueryGraphResponseObject, error) {
	g, err := s.svc.QueryGraph(ctx, str(r.Params.Filter), num(r.Params.Budget, graph.DefaultBudget))
	if err == nil {
		var out Graph
		if out, err = convert[Graph](g); err == nil {
			return GetQueryGraph200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	if st == http.StatusBadRequest {
		return GetQueryGraph400JSONResponse{BadRequestJSONResponse(body)}, nil
	}
	return GetQueryGraphdefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// GetStale reports the staleness mix.
func (s *Server) GetStale(ctx context.Context, r GetStaleRequestObject) (GetStaleResponseObject, error) {
	t, err := s.svc.Stale(ctx, str(r.Params.GroupBy))
	if err == nil {
		var out Table
		if out, err = convert[Table](t); err == nil {
			return GetStale200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	if st == http.StatusBadRequest {
		return GetStale400JSONResponse{BadRequestJSONResponse(body)}, nil
	}
	return GetStaledefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// GetRoots lists root bases.
func (s *Server) GetRoots(ctx context.Context, _ GetRootsRequestObject) (GetRootsResponseObject, error) {
	roots, err := s.svc.Roots(ctx)
	if err == nil {
		var out []Root
		if out, err = convert[[]Root](roots); err == nil {
			if out == nil {
				out = []Root{}
			}
			return GetRoots200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	return GetRootsdefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// GetSummary reports footprint metrics.
func (s *Server) GetSummary(ctx context.Context, r GetSummaryRequestObject) (GetSummaryResponseObject, error) {
	sum, err := s.svc.Summary(ctx, str(r.Params.GroupBy))
	if err == nil {
		var out Summary
		if out, err = convert[Summary](sum); err == nil {
			return GetSummary200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	if st == http.StatusBadRequest {
		return GetSummary400JSONResponse{BadRequestJSONResponse(body)}, nil
	}
	return GetSummarydefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// ReadHeaderTimeout bounds slow clients.
const ReadHeaderTimeout = 10 * time.Second
