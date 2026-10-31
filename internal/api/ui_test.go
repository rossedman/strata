package api

import (
	"testing"

	"github.com/rossedman/strata/pkg/client"
)

func TestUIEndpoints(t *testing.T) {
	t.Parallel()
	c, _ := serve(t, Options{})
	o, err := c.GetOverviewWithResponse(ctx)
	if err != nil || o.JSON200 == nil || o.JSON200.Images != 5 {
		t.Fatalf("overview = %d %s", o.StatusCode(), o.Body)
	}
	status, err := c.GetStatusWithResponse(ctx)
	if err != nil || status.JSON200 == nil || status.JSON200.Running {
		t.Fatalf("status = %d %s", status.StatusCode(), status.Body)
	}
	roots, err := c.GetOutlineWithResponse(ctx, nil)
	if err != nil || roots.JSON200 == nil || len(roots.JSON200.Rows) == 0 {
		t.Fatalf("outline roots = %d %s", roots.StatusCode(), roots.Body)
	}
	id, stale, limit := roots.JSON200.Rows[0].Id, true, 10
	kids, err := c.GetOutlineWithResponse(ctx, &client.GetOutlineParams{Parent: &id, OnlyStale: &stale, Limit: &limit})
	if err != nil || kids.JSON200 == nil {
		t.Fatalf("outline children = %d %s", kids.StatusCode(), kids.Body)
	}
	bad := "repo|broken"
	if b, _ := c.GetOutlineWithResponse(ctx, &client.GetOutlineParams{Parent: &bad}); b.JSON400 == nil {
		t.Fatalf("bad outline id = %d", b.StatusCode())
	}
	filter, teams := "layers >= 1", []string{"payments"}
	ages := []client.SearchParamsAge{"> 1 year"}
	s, err := c.SearchWithResponse(ctx, &client.SearchParams{Filter: &filter, Team: &teams, Age: &ages})
	if err != nil || s.JSON200 == nil || s.JSON200.Total != 2 || len(s.JSON200.Facets) != 5 {
		t.Fatalf("search = %d %s", s.StatusCode(), s.Body)
	}
	nope := "nope = 1"
	if b, _ := c.SearchWithResponse(ctx, &client.SearchParams{Filter: &nope}); b.JSON400 == nil {
		t.Fatalf("bad search = %d", b.StatusCode())
	}
	m, err := c.GetMapWithResponse(ctx, nil)
	if err != nil || m.JSON200 == nil || len(*m.JSON200) == 0 {
		t.Fatalf("map = %d %s", m.StatusCode(), m.Body)
	}
}

func TestChartEndpoints(t *testing.T) {
	t.Parallel()
	c, _ := serve(t, Options{})
	if k, err := c.GetKPIHistoryWithResponse(ctx, nil); err != nil || k.JSON200 == nil || len(*k.JSON200) != 1 {
		t.Fatalf("kpis = %d %s", k.StatusCode(), k.Body)
	}
	repo := client.GetStorageHistoryParamsGroupBy("repo")
	if s, err := c.GetStorageHistoryWithResponse(ctx, &client.GetStorageHistoryParams{GroupBy: &repo}); err != nil || s.JSON200 == nil || len(s.JSON200.Series) == 0 {
		t.Fatalf("storage = %d %s", s.StatusCode(), s.Body)
	}
	if a, err := c.GetActivityWithResponse(ctx, nil); err != nil || a.JSON200 == nil || a.JSON200.Source != "created" {
		t.Fatalf("activity = %d %s", a.StatusCode(), a.Body)
	}
	if a, err := c.GetAgesWithResponse(ctx); err != nil || a.JSON200 == nil || len(a.JSON200.Bins) == 0 {
		t.Fatalf("ages = %d %s", a.StatusCode(), a.Body)
	}
	if cv, err := c.GetBaseCoverageWithResponse(ctx); err != nil || cv.JSON200 == nil || len(*cv.JSON200) != 1 {
		t.Fatalf("coverage = %d %s", cv.StatusCode(), cv.Body)
	}
	ad, err := c.GetAdoptionWithResponse(ctx, nil)
	if err != nil || ad.JSON200 == nil || ad.JSON200.Base == "" {
		t.Fatalf("adoption = %d %s", ad.StatusCode(), ad.Body)
	}
	if r, err := c.GetRolloutWithResponse(ctx, &client.GetRolloutParams{Base: ad.JSON200.Base}); err != nil || r.JSON200 == nil || len(r.JSON200.Days) != 1 {
		t.Fatalf("rollout = %d %s", r.StatusCode(), r.Body)
	}
	if r, _ := c.GetRolloutWithResponse(ctx, &client.GetRolloutParams{Base: ""}); r.JSON400 == nil {
		t.Fatalf("rollout without a base = %d", r.StatusCode())
	}
}
