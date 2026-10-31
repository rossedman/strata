package api

import (
	"bytes"
	"context"
	"net/http"

	"github.com/rossedman/strata/internal/render"
	"github.com/rossedman/strata/internal/service"
)

// GetCleanup lists deletion candidates, as JSON or as a CSV export.
func (s *Server) GetCleanup(ctx context.Context, r GetCleanupRequestObject) (GetCleanupResponseObject, error) {
	p := r.Params
	csv := str(p.Format) == string(Csv)
	opts := service.CleanupOptions{Where: str(p.Where), Scope: str(p.Scope)}
	if !csv {
		opts.Limit = num(p.Limit, 0)
	}
	c, err := s.svc.Cleanup(ctx, opts)
	if err == nil && csv {
		rows := make([][]any, len(c.Candidates))
		for i, cc := range c.Candidates {
			rows[i] = cc.Row()
		}
		var buf bytes.Buffer
		if err = render.WriteRows(&buf, render.CSV, service.CleanupColumns, rows); err == nil {
			return GetCleanup200TextcsvResponse{Body: &buf, ContentLength: int64(buf.Len())}, nil
		}
	}
	if err == nil {
		var out Cleanup
		if out, err = convert[Cleanup](c); err == nil {
			return GetCleanup200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	if st == http.StatusBadRequest {
		return GetCleanup400JSONResponse{BadRequestJSONResponse(body)}, nil
	}
	return GetCleanupdefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// GetLifecycles generates ECR lifecycle policies.
func (s *Server) GetLifecycles(ctx context.Context, r GetLifecyclesRequestObject) (GetLifecyclesResponseObject, error) {
	p := r.Params
	lcs, err := s.svc.Lifecycles(ctx, service.LifecycleOptions{Repos: list(p.Repo), Days: num(p.Days, 0), IncludeConflicts: p.IncludeConflicts != nil && *p.IncludeConflicts})
	if err == nil {
		var out []Lifecycle
		if out, err = convert[[]Lifecycle](lcs); err == nil {
			if out == nil {
				out = []Lifecycle{}
			}
			return GetLifecycles200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	if st == http.StatusBadRequest {
		return GetLifecycles400JSONResponse{BadRequestJSONResponse(body)}, nil
	}
	return GetLifecyclesdefaultJSONResponse{Body: body, StatusCode: st}, nil
}
