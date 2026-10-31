package api

import (
	"bytes"
	"context"
	"net/http"

	"github.com/rossedman/strata/internal/render"
	"github.com/rossedman/strata/internal/service"
)

// GetVulnBases ranks the vulnerable bases.
func (s *Server) GetVulnBases(ctx context.Context, r GetVulnBasesRequestObject) (GetVulnBasesResponseObject, error) {
	p := r.Params
	bases, err := s.svc.VulnBases(ctx, service.VulnBasesOptions{
		Severity: str(p.Severity), GroupBy: str(p.GroupBy), InUseDays: num(p.InUseDays, 0), Limit: num(p.Limit, 0),
	})
	if err == nil {
		var out []VulnBase
		if out, err = convert[[]VulnBase](bases); err == nil {
			if out == nil {
				out = []VulnBase{}
			}
			return GetVulnBases200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	if st == http.StatusBadRequest {
		return GetVulnBases400JSONResponse{BadRequestJSONResponse(body)}, nil
	}
	return GetVulnBasesdefaultJSONResponse{Body: body, StatusCode: st}, nil
}

// GetVulnImpact reports the blast radius of a base or a vulnerability,
// as JSON or as a CSV export of every affected image.
func (s *Server) GetVulnImpact(ctx context.Context, r GetVulnImpactRequestObject) (GetVulnImpactResponseObject, error) {
	p := r.Params
	im, err := s.svc.Impact(ctx, service.ImpactOptions{
		Ref: str(p.Ref), CVE: str(p.Cve), Filter: str(p.Filter), GroupBy: str(p.GroupBy), InUseDays: num(p.InUseDays, 0),
	})
	if err == nil && str(p.Format) == string(GetVulnImpactParamsFormatCsv) {
		rows := make([][]any, len(im.Images))
		for i, img := range im.Images {
			rows[i] = img.Row()
		}
		var buf bytes.Buffer
		if err = render.WriteRows(&buf, render.CSV, service.ImpactColumns, rows); err == nil {
			return GetVulnImpact200TextcsvResponse{Body: &buf, ContentLength: int64(buf.Len())}, nil
		}
	}
	if err == nil {
		if limit := num(p.Limit, 1000); len(im.Images) > limit {
			im.Images = im.Images[:limit]
		}
		var out Impact
		if out, err = convert[Impact](im); err == nil {
			return GetVulnImpact200JSONResponse(out), nil
		}
	}
	st, body := classify(err)
	switch st {
	case http.StatusBadRequest:
		return GetVulnImpact400JSONResponse{BadRequestJSONResponse(body)}, nil
	case http.StatusNotFound:
		return GetVulnImpact404JSONResponse{NotFoundJSONResponse(body)}, nil
	}
	return GetVulnImpactdefaultJSONResponse{Body: body, StatusCode: st}, nil
}
