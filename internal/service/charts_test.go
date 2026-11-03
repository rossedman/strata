package service

import (
	"strings"
	"testing"
	"time"
)

func TestChartsWithoutPushTimes(t *testing.T) {
	t.Parallel()
	e := setup(t)
	h := e.reg.Host

	// The test registry reports no push times, so charts date images by
	// their build time.
	act, err := e.svc.Activity(ctx, "", 4000)
	if err != nil {
		t.Fatal(err)
	}
	if act.Source != "created" || len(act.Repos) == 0 {
		t.Fatalf("activity = %+v", act)
	}
	var total int64
	for _, d := range act.Days {
		total += d.Count
	}
	if total == 0 {
		t.Fatalf("no images dated: %+v", act)
	}
	one, err := e.svc.Activity(ctx, h+"/payments/api", 4000)
	if err != nil {
		t.Fatal(err)
	}
	if one.LastPush == "" || len(one.Days) == 0 || len(one.Days) > len(act.Days) {
		t.Fatalf("one repo = %+v", one)
	}

	ages, err := e.svc.Ages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ages.Source != "created" || len(ages.Bins) < 7 || ages.Bins[0].Filter != "age < 7d" || ages.Bins[6].Filter != "age >= 730d" ||
		ages.Bins[2].Filter != "age >= 30d and age < 90d" {
		t.Fatalf("ages = %+v", ages)
	}
	var binned int64
	for _, b := range ages.Bins {
		binned += b.Unrated + b.Verdicts.Fresh + b.Verdicts.Drifting + b.Verdicts.Stale + b.Verdicts.Abandoned
	}
	o, err := e.svc.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if binned != o.Images {
		t.Fatalf("ages bin %d images, index has %d", binned, o.Images)
	}
	// Every bin's filter is valid query language.
	for _, b := range ages.Bins {
		if _, err := e.svc.Search(ctx, SearchOptions{Filter: b.Filter, Limit: 1}); err != nil {
			t.Fatalf("filter %q: %v", b.Filter, err)
		}
	}

	cov, err := e.svc.BaseCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cov) != 1 || cov[0].Registry != h {
		t.Fatalf("coverage = %+v", cov)
	}
	c := cov[0]
	if c.Layers == 0 || c.Roots == 0 || c.Layers+c.Provenance+c.Declared+c.Named+c.Unknown+c.Roots != c.Images || c.Images != o.Images {
		t.Fatalf("coverage = %+v (images %d)", c, o.Images)
	}

	ad, err := e.svc.Adoption(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if ad.Base == "" || len(ad.Bases) == 0 || ad.Bases[0].Repo != ad.Base || len(ad.Releases) == 0 {
		t.Fatalf("adoption = %+v", ad)
	}
	goBase, err := e.svc.Adoption(ctx, h+"/base/go")
	if err != nil {
		t.Fatal(err)
	}
	// base/go was rebuilt: api v2 is on the new release, api v1 and the
	// worker on the old one.
	if len(goBase.Releases) != 2 || goBase.Releases[0].Behind != 0 || goBase.Releases[1].Behind < 1 ||
		goBase.Releases[0].Images != 1 || goBase.Releases[1].Images < 2 {
		t.Fatalf("base/go adoption = %+v", goBase.Releases)
	}
	if !slicesHas(goBase.Releases[0].Tags, h+"/base/go:1.22") {
		t.Fatalf("newest release tags = %v", goBase.Releases[0].Tags)
	}
	if none, err := e.svc.Adoption(ctx, h+"/nope"); err != nil || len(none.Releases) != 0 {
		t.Fatalf("unknown base = %+v, %v", none, err)
	}
}

func slicesHas(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestChartsWithPushTimes(t *testing.T) {
	t.Parallel()
	e := setup(t)
	now := time.Now().UTC()
	if _, err := e.svc.db().ExecContext(ctx, `INSERT INTO pushes SELECT repo, digest, ?::TIMESTAMPTZ - INTERVAL 3 DAY, NULL FROM images`, now); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	act, err := e.svc.Activity(ctx, "", 30)
	if err != nil {
		t.Fatal(err)
	}
	if act.Source != "pushed" || len(act.Days) != 1 || act.Days[0].Day != now.AddDate(0, 0, -3).Format(time.DateOnly) {
		t.Fatalf("activity = %+v", act)
	}
	ages, err := e.svc.Ages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ages.Source != "pushed" || ages.Bins[0].Filter != "pushed_age < 7d" || sumVerdicts(ages.Bins[0]) == 0 || len(ages.Bins) != 7 {
		t.Fatalf("ages = %+v", ages)
	}
	if _, err := e.svc.Search(ctx, SearchOptions{Filter: ages.Bins[0].Filter}); err != nil {
		t.Fatal(err)
	}
}

func sumVerdicts(b AgeBin) int64 {
	return b.Unrated + b.Verdicts.Fresh + b.Verdicts.Drifting + b.Verdicts.Stale + b.Verdicts.Abandoned
}

func TestHistoryCharts(t *testing.T) {
	t.Parallel()
	e := setup(t)
	h := e.reg.Host

	// Snapshots on three days: today's comes from setup; earlier days are
	// written with the clock turned back.
	today := time.Now().UTC()
	for _, back := range []int{2, 1} {
		day := today.AddDate(0, 0, -back)
		e.svc.Now = func() time.Time { return day }
		if err := e.svc.Snapshot(ctx); err != nil {
			t.Fatal(err)
		}
	}
	e.svc.Now = time.Now

	kpi, err := e.svc.KPIHistory(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(kpi) != 3 || kpi[2].Images == 0 || kpi[2].DedupBytes == 0 || kpi[2].Repos == 0 {
		t.Fatalf("kpi = %+v", kpi)
	}

	st, err := e.svc.Storage(ctx, "root", 1, 30)
	if err != nil {
		t.Fatal(err)
	}
	if st.GroupBy != "root" || len(st.Days) != 3 || len(st.Series) != 2 || !st.Series[1].Other || st.Series[0].Other {
		t.Fatalf("storage = %+v", st)
	}
	// The series split the deduplicated total between them.
	var sum int64
	for _, ser := range st.Series {
		sum += ser.Values[2]
	}
	if diff := sum - kpi[2].DedupBytes; diff < -2 || diff > 2 {
		t.Fatalf("storage series add to %d, dedup total is %d", sum, kpi[2].DedupBytes)
	}
	if repos, err := e.svc.Storage(ctx, "repo", 0, 0); err != nil || repos.GroupBy != "repo" || len(repos.Series) == 0 {
		t.Fatalf("storage by repo = %+v, %v", repos, err)
	}
	if _, err := e.svc.Storage(ctx, "platform", 0, 0); err == nil || !IsInputError(err) {
		t.Fatalf("storage by platform: %v", err)
	}
	if def, err := e.svc.Storage(ctx, "", 0, 0); err != nil || def.GroupBy != "root" {
		t.Fatalf("default storage = %+v, %v", def, err)
	}

	ro, err := e.svc.Rollout(ctx, h+"/base/go", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(ro.Days) != 3 || len(ro.Series) != 2 {
		t.Fatalf("rollout = %+v", ro)
	}
	newest := ro.Series[0]
	if newest.Older || !slicesHas(newest.Tags, h+"/base/go:1.22") || newest.Counts[2] != 1 {
		t.Fatalf("newest series = %+v", newest)
	}
	if _, err := e.svc.Rollout(ctx, "", 30); err == nil || !IsInputError(err) {
		t.Fatalf("rollout without a base: %v", err)
	}
}

func TestLayerOrigins(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ex, err := e.svc.Explain(ctx, e.reg.Host+"/payments/api:1.0", "", false)
	if err != nil {
		t.Fatal(err)
	}
	// api 1.0: debian's layer, go's layer, then its own.
	got := make([]string, len(ex.Layers))
	for i, l := range ex.Layers {
		got[i] = l.AddedBy
	}
	if got[0] != e.reg.Digest("debian") || got[1] != e.oldGoAMD || got[2] != "" {
		t.Fatalf("added by = %s", strings.Join(got, ", "))
	}
}
