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
