package api

import (
	"context"
	"net/http"

	"github.com/rossedman/strata/internal/service"
)

// list dereferences an optional string list.
func list(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

// GetOverview returns the headline numbers for the web UI.
func (s *Server) GetOverview(ctx context.Context, _ GetOverviewRequestObject) (GetOverviewResponseObject, error) {
	o, err := s.svc.Overview(ctx)
	if err == nil {
		var out Overview
		if out, err = convert[Overview](o); err == nil {
			return GetOverview200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	return GetOverviewdefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// GetOutline returns one level of the lineage outline.
func (s *Server) GetOutline(ctx context.Context, r GetOutlineRequestObject) (GetOutlineResponseObject, error) {
	p := r.Params
	o, err := s.svc.Outline(ctx, str(p.Parent), p.OnlyStale != nil && *p.OnlyStale, num(p.Offset, 0), num(p.Limit, 50))
	if err == nil {
		var out Outline
		if out, err = convert[Outline](o); err == nil {
			return GetOutline200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	if st == http.StatusBadRequest {
		return GetOutline400JSONResponse{BadRequestJSONResponse(body)}, nil
	}
	return GetOutlinedefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// Search runs a filter with facet selections.
func (s *Server) Search(ctx context.Context, r SearchRequestObject) (SearchResponseObject, error) {
	p := r.Params
	var ages, pushed []string
	if p.Age != nil {
		for _, a := range *p.Age {
			ages = append(ages, string(a))
		}
	}
	if p.Pushed != nil {
		for _, a := range *p.Pushed {
			pushed = append(pushed, string(a))
		}
	}
	res, err := s.svc.Search(ctx, service.SearchOptions{
		Filter: str(p.Filter), Team: list(p.Team), Root: list(p.Root), Verdict: list(p.Verdict),
		Platform: list(p.Platform), Age: ages, Pushed: pushed, Offset: num(p.Offset, 0), Limit: num(p.Limit, 50),
	})
	if err == nil {
		var out SearchResult
		if out, err = convert[SearchResult](res); err == nil {
			return Search200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	if st == http.StatusBadRequest {
		return Search400JSONResponse{BadRequestJSONResponse(body)}, nil
	}
	return SearchdefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// GetMap returns the aggregated registry map.
func (s *Server) GetMap(ctx context.Context, r GetMapRequestObject) (GetMapResponseObject, error) {
	rows, err := s.svc.Map(ctx, num(r.Params.Depth, 3))
	if err == nil {
		var out []OutlineRow
		if out, err = convert[[]OutlineRow](rows); err == nil {
			if out == nil {
				out = []OutlineRow{}
			}
			return GetMap200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	return GetMapdefaultJSONResponse{Body: body, StatusCode: st}, nil
}
