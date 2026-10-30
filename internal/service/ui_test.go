package service

import (
	"strings"
	"testing"
)

func TestOutline(t *testing.T) {
	t.Parallel()
	e := setup(t)
	roots, err := e.svc.Outline(ctx, "", false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots.Rows) == 0 || roots.Total != int64(len(roots.Rows)) {
		t.Fatalf("roots = %+v", roots)
	}
	debian := roots.Rows[0]
	if debian.Kind != "base" || debian.Images < 5 || !debian.HasChildren || !strings.Contains(debian.Name, "base/debian") {
		t.Fatalf("first root = %+v", debian)
	}
	if sum := debian.Verdicts.Fresh + debian.Verdicts.Drifting + debian.Verdicts.Stale + debian.Verdicts.Abandoned; sum != debian.Images {
		t.Fatalf("verdicts %+v do not add up to %d images", debian.Verdicts, debian.Images)
	}

	// debian's children: the old and new go bases (each with children),
	// grouped leaves per repo.
	kids, err := e.svc.Outline(ctx, debian.ID, false, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var base, repo *OutlineRow
	for i := range kids.Rows {
		switch kids.Rows[i].Kind {
		case "base":
			if base == nil || kids.Rows[i].Images > base.Images {
				base = &kids.Rows[i]
			}
		case "repo":
			repo = &kids.Rows[i]
		}
	}
	if base == nil {
		t.Fatalf("no base rows under debian: %+v", kids.Rows)
	}
	// Under the old go base sit the api and worker repos as groups.
	grand, err := e.svc.Outline(ctx, base.ID, false, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range grand.Rows {
		if r.Kind == "repo" {
			repo = &r
			break
		}
	}
	if repo == nil || repo.Team == "" || !strings.HasPrefix(repo.ID, "repo|") {
		t.Fatalf("repo row = %+v (rows %+v)", repo, grand.Rows)
	}
	imgs, err := e.svc.Outline(ctx, repo.ID, false, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs.Rows) != 1 || imgs.Rows[0].Kind != "image" || imgs.Rows[0].Digest == "" || imgs.Total < 1 {
		t.Fatalf("images = %+v", imgs)
	}
	if more, err := e.svc.Outline(ctx, repo.ID, false, 1, 1); err != nil || more.Offset != 1 {
		t.Fatalf("second page = %+v, %v", more, err)
	}

	// The worker's v1 tag is untagged nowhere, so only-stale filters rows
	// with no stale or abandoned images.
	stale, err := e.svc.Outline(ctx, "", true, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range stale.Rows {
		if r.Verdicts.Stale+r.Verdicts.Abandoned == 0 {
			t.Fatalf("only-stale returned a clean row: %+v", r)
		}
	}
	if _, err := e.svc.Outline(ctx, "repo|broken", false, 0, 10); !IsInputError(err) {
		t.Fatalf("bad repo id error = %v", err)
	}
	if _, err := e.svc.Outline(ctx, Unrooted, false, 0, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Outline(ctx, "repo|"+Unrooted+"|nope", true, 0, 10); err != nil {
		t.Fatal(err)
	}
}

func TestMapAndOverview(t *testing.T) {
	t.Parallel()
	e := setup(t)
	rows, err := e.svc.Map(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	parents := map[string]bool{}
	for _, r := range rows {
		parents[r.Parent] = true
	}
	if len(rows) < 4 || !parents[""] || len(parents) < 2 {
		t.Fatalf("map rows = %d, parents %v", len(rows), parents)
	}
	o, err := e.svc.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.Images < 6 || o.Repos < 4 || o.Host != e.reg.Host || o.IndexedAt == nil || len(o.Roots) == 0 {
		t.Fatalf("overview = %+v", o)
	}
	if v := o.Verdicts; v.Fresh+v.Drifting+v.Stale+v.Abandoned != o.Images {
		t.Fatalf("overview verdicts %+v do not add up to %d", v, o.Images)
	}
}

func TestSearch(t *testing.T) {
	t.Parallel()
	e := setup(t)
	all, err := e.svc.Search(ctx, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total < 6 || len(all.Facets) != 5 || all.Filter != "" {
		t.Fatalf("unfiltered search = total %d, %d facets, filter %q", all.Total, len(all.Facets), all.Filter)
	}
	teams := all.Facets[1]
	if teams.Name != "Team" || len(teams.Values) == 0 {
		t.Fatalf("team facet = %+v", teams)
	}
	res, err := e.svc.Search(ctx, SearchOptions{Filter: "layers >= 1", Team: []string{"payments"}, Age: []string{"> 1 year", "< 30 days"}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total < 1 || len(res.Hits) != 1 || res.Hits[0].Team != "payments" {
		t.Fatalf("faceted search = %+v", res)
	}
	if res.Filter != `layers >= 1 and label.team in ["payments"] and (age >= 1y or age < 30d)` {
		t.Fatalf("folded filter = %q", res.Filter)
	}
	// A facet's own selection does not narrow its own counts.
	one, err := e.svc.Search(ctx, SearchOptions{Verdict: []string{"fresh"}})
	if err != nil {
		t.Fatal(err)
	}
	if v := one.Facets[0]; v.Name != "Verdict" || len(v.Values) < 2 {
		t.Fatalf("verdict facet hid the other verdicts: %+v", v.Values)
	}
	past, err := e.svc.Search(ctx, SearchOptions{Offset: 10000})
	if err != nil || len(past.Hits) != 0 || past.Total != all.Total {
		t.Fatalf("past the end = %+v, %v", past, err)
	}
	selected, err := e.svc.Search(ctx, SearchOptions{Verdict: []string{"abandoned"}, Platform: []string{"plan9/mips"}, Root: []string{"nope"}})
	if err != nil || selected.Total != 0 {
		t.Fatalf("impossible selection = %+v, %v", selected, err)
	}
	for _, f := range selected.Facets {
		if f.Name == "Platform" {
			found := false
			for _, v := range f.Values {
				found = found || (v.Value == "plan9/mips" && v.Selected)
			}
			if !found {
				t.Fatalf("selected platform missing from facet: %+v", f.Values)
			}
		}
	}
	if _, err := e.svc.Search(ctx, SearchOptions{Filter: "nope = 1"}); !IsInputError(err) {
		t.Fatalf("bad filter error = %v", err)
	}
}

func TestFoldFilter(t *testing.T) {
	t.Parallel()
	got := foldFilter(`a = 1 or b = 2`, []facetDef{{field: "stale", selected: []string{"stale"}}, {field: "age", selected: []string{"unknown"}}})
	if got != `(a = 1 or b = 2) and stale in ["stale"] and (not age exists)` {
		t.Fatalf("foldFilter = %q", got)
	}
	got = foldFilter("", []facetDef{{field: "pushed_age", selected: []string{"< 30 days", "bogus"}}})
	if got != `(pushed_age < 30d)` {
		t.Fatalf("foldFilter = %q", got)
	}
	if foldFilter("", nil) != "" {
		t.Fatal("empty fold")
	}
}

func TestSearchPushedFacet(t *testing.T) {
	t.Parallel()
	e := setup(t)
	// No push times: the facet is left out.
	res, err := e.svc.Search(ctx, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Facets {
		if f.Name == "Pushed" {
			t.Fatalf("pushed facet without push times: %+v", f)
		}
	}
	var digest string
	if err := e.svc.db().QueryRow(`SELECT digest FROM images ORDER BY digest LIMIT 1`).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.db().Exec(`INSERT INTO pushes VALUES ('r', ?, now() - INTERVAL 3 DAY, NULL)`, digest); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	res, err = e.svc.Search(ctx, SearchOptions{Pushed: []string{"< 30 days"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || res.Hits[0].Digest != digest || res.Filter != "(pushed_age < 30d)" {
		t.Fatalf("pushed search = %+v", res)
	}
	if f := res.Facets[len(res.Facets)-1]; f.Name != "Pushed" || f.Values[0].Value != "< 30 days" || !f.Values[0].Selected {
		t.Fatalf("pushed facet = %+v", f)
	}
	ex, err := e.svc.Explain(ctx, digest, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if ex.Image.Pushed == nil || ex.Image.FirstSeen == nil {
		t.Fatalf("explain dates = %+v", ex.Image)
	}
}
