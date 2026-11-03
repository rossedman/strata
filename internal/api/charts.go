package api

import (
	"context"
	"net/http"
)

// GetKPIHistory returns the Overview's numbers per snapshot day.
func (s *Server) GetKPIHistory(ctx context.Context, r GetKPIHistoryRequestObject) (GetKPIHistoryResponseObject, error) {
	days, err := s.svc.KPIHistory(ctx, num(r.Params.Days, 30))
	if err == nil {
		var out []KPIDay
		if out, err = convert[[]KPIDay](days); err == nil {
			return GetKPIHistory200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	return GetKPIHistorydefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// GetStorageHistory returns stored bytes over time by root or repo.
func (s *Server) GetStorageHistory(ctx context.Context, r GetStorageHistoryRequestObject) (GetStorageHistoryResponseObject, error) {
	p := r.Params
	res, err := s.svc.Storage(ctx, str(p.GroupBy), num(p.Top, 6), num(p.Days, 180))
	if err == nil {
		var out Storage
		if out, err = convert[Storage](res); err == nil {
			return GetStorageHistory200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	if st == http.StatusBadRequest {
		return GetStorageHistory400JSONResponse{BadRequestJSONResponse(body)}, nil
	}
	return GetStorageHistorydefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// GetActivity returns images pushed per day.
func (s *Server) GetActivity(ctx context.Context, r GetActivityRequestObject) (GetActivityResponseObject, error) {
	res, err := s.svc.Activity(ctx, str(r.Params.Repo), num(r.Params.Days, 182))
	if err == nil {
		var out Activity
		if out, err = convert[Activity](res); err == nil {
			return GetActivity200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	return GetActivitydefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// GetAges returns images by age and verdict.
func (s *Server) GetAges(ctx context.Context, _ GetAgesRequestObject) (GetAgesResponseObject, error) {
	res, err := s.svc.Ages(ctx)
	if err == nil {
		var out Ages
		if out, err = convert[Ages](res); err == nil {
			return GetAges200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	return GetAgesdefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// GetBaseCoverage returns how images were linked to their bases.
func (s *Server) GetBaseCoverage(ctx context.Context, _ GetBaseCoverageRequestObject) (GetBaseCoverageResponseObject, error) {
	res, err := s.svc.BaseCoverage(ctx)
	if err == nil {
		var out []Coverage
		if out, err = convert[[]Coverage](res); err == nil {
			return GetBaseCoverage200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	return GetBaseCoveragedefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// GetAdoption returns which releases of a base images are built on.
func (s *Server) GetAdoption(ctx context.Context, r GetAdoptionRequestObject) (GetAdoptionResponseObject, error) {
	res, err := s.svc.Adoption(ctx, str(r.Params.Base))
	if err == nil {
		var out Adoption
		if out, err = convert[Adoption](res); err == nil {
			return GetAdoption200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	return GetAdoptiondefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// GetRollout returns how images moved between a base's releases.
func (s *Server) GetRollout(ctx context.Context, r GetRolloutRequestObject) (GetRolloutResponseObject, error) {
	res, err := s.svc.Rollout(ctx, r.Params.Base, num(r.Params.Days, 60))
	if err == nil {
		var out Rollout
		if out, err = convert[Rollout](res); err == nil {
			return GetRollout200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	if st == http.StatusBadRequest {
		return GetRollout400JSONResponse{BadRequestJSONResponse(body)}, nil
	}
	return GetRolloutdefaultJSONResponse{Body: body, StatusCode: st}, nil
}
