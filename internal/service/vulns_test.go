package service

import (
	"testing"
	"time"

	"github.com/rossedman/strata/internal/fixture"
	"github.com/rossedman/strata/internal/model"
	"github.com/rossedman/strata/internal/store"
)

// vulnEnv indexes a small tree: debian, go on debian, and four apps on
// go owned by two teams, with scans that carry CVE-A down part of it.
func vulnEnv(t *testing.T) (*Service, map[string]*model.Image) {
	t.Helper()
	imgs := map[string]*model.Image{
		"debian": fixture.Image("debian", "reg/base/debian", "d"),
		"go":     fixture.Image("go", "reg/base/go", "d", "g"),
		"api":    fixture.Label(fixture.Image("api", "reg/pay/api", "d", "g", "api"), "team", "payments"),
		"worker": fixture.Label(fixture.Image("worker", "reg/pay/worker", "d", "g", "worker"), "team", "payments"),
		"site":   fixture.Label(fixture.Image("site", "reg/web/site", "d", "g", "site"), "team", "web"),
		"cron":   fixture.Label(fixture.Image("cron", "reg/web/cron", "d", "g", "site", "cron"), "team", "web"),
		"tool":   fixture.Image("tool", "reg/ops/tool", "t"),
	}
	now := fixture.Epoch
	findings := func(ids ...string) []model.Finding {
		var out []model.Finding
		for _, id := range ids {
			out = append(out, model.Finding{ID: id, Package: "pkg-" + id, Severity: "critical", FixedIn: "2"})
		}
		return out
	}
	scan := func(name string, crit, high int, ids ...string) model.Scan {
		return model.Scan{Digest: imgs[name].Digest, Status: model.ScanComplete, CompletedAt: now, Critical: crit, High: high,
			HasFindings: true, FetchedAt: now, Findings: findings(ids...)}
	}
	b := &store.Batch{
		Scans: []model.Scan{
			scan("debian", 1, 0, "CVE-A"),
			scan("go", 2, 1, "CVE-A", "CVE-B"),
			// api was patched; worker still has it; site and cron were
			// never scanned. tool has its own CVE-A and nothing on it.
			scan("api", 0, 1),
			scan("worker", 2, 0, "CVE-A", "CVE-B"),
			scan("tool", 1, 0, "CVE-A"),
		},
		Pushes: []model.Push{
			{Repo: "reg/pay/worker", Digest: imgs["worker"].Digest, PushedAt: now.AddDate(0, 0, -100), LastPulled: now.AddDate(0, 0, -1)},
			{Repo: "reg/web/site", Digest: imgs["site"].Digest, PushedAt: now.AddDate(0, 0, -100), LastPulled: now.AddDate(0, 0, -90)},
		},
	}
	for _, n := range []string{"debian", "go", "api", "worker", "site", "cron", "tool"} {
		b.Images = append(b.Images, imgs[n])
		b.Tags = append(b.Tags, fixture.Tag(imgs[n], "v1", 0))
	}
	svc := New(fixture.Open(t, b))
	svc.Now = func() time.Time { return now }
	if err := svc.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	return svc, imgs
}

func TestVulnBases(t *testing.T) {
	t.Parallel()
	svc, _ := vulnEnv(t)
	bases, err := svc.VulnBases(ctx, VulnBasesOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// go reaches 4 images (1 in use) with 2 criticals; debian reaches 5
	// (1 in use) with 1. site has no scan, so it is not ranked; tool has
	// nothing built on it.
	if len(bases) != 2 || bases[0].Ref != "reg/base/go:v1" || bases[1].Ref != "reg/base/debian:v1" {
		t.Fatalf("bases = %+v", bases)
	}
	g := bases[0]
	if g.Critical != 2 || g.High != 1 || g.Images != 4 || g.InUse != 1 || g.Vulnerable != 1 || g.Groups != 2 || g.Fixable == nil || *g.Fixable != 2 {
		t.Fatalf("go = %+v", g)
	}
	high, err := svc.VulnBases(ctx, VulnBasesOptions{Severity: "high", Limit: 1, GroupBy: "repo"})
	if err != nil || len(high) != 1 || high[0].Ref != "reg/base/go:v1" || high[0].Groups != 4 || high[0].Vulnerable != 1 {
		t.Fatalf("high = %+v, %v", high, err)
	}
	if _, err := svc.VulnBases(ctx, VulnBasesOptions{Severity: "spicy"}); !IsInputError(err) {
		t.Fatalf("bad severity: %v", err)
	}
	if _, err := svc.VulnBases(ctx, VulnBasesOptions{GroupBy: "tag"}); !IsInputError(err) {
		t.Fatalf("list group-by: %v", err)
	}
}

func TestImpact(t *testing.T) {
	t.Parallel()
	svc, _ := vulnEnv(t)

	// Everything built on go, worker (pulled yesterday) first.
	im, err := svc.Impact(ctx, ImpactOptions{Ref: "reg/base/go:v1"})
	if err != nil {
		t.Fatal(err)
	}
	if im.Total != 4 || im.InUse != 1 || im.Images[0].Repo != "reg/pay/worker" || !im.Images[0].InUse || im.Images[0].Evidence != "built on" {
		t.Fatalf("impact of go = %+v", im)
	}
	if len(im.Origins) != 1 || im.Origins[0].Ref != "reg/base/go:v1" || im.Origins[0].Images != 4 {
		t.Fatalf("origins = %+v", im.Origins)
	}
	if len(im.Groups) != 2 || im.Groups[0] != (ImpactGroup{Value: "payments", Images: 2, InUse: 1, Repos: 2}) || im.Groups[1] != (ImpactGroup{Value: "web", Images: 2, Repos: 2}) {
		t.Fatalf("groups = %+v", im.Groups)
	}

	// CVE-A enters through debian (whose parent is none) and tool; api
	// was patched so the walk stops there; site and cron are unscanned
	// and inherit it from go.
	im, err = svc.Impact(ctx, ImpactOptions{CVE: "CVE-A"})
	if err != nil {
		t.Fatal(err)
	}
	evidence := map[string]string{}
	for _, i := range im.Images {
		evidence[i.Repo] = i.Evidence
	}
	want := map[string]string{"reg/base/debian": "scan", "reg/base/go": "scan", "reg/pay/worker": "scan", "reg/web/site": "inherited", "reg/web/cron": "inherited", "reg/ops/tool": "scan"}
	if len(evidence) != len(want) {
		t.Fatalf("affected = %v", evidence)
	}
	for k, v := range want {
		if evidence[k] != v {
			t.Fatalf("affected = %v", evidence)
		}
	}
	if len(im.Origins) != 2 || im.Origins[0].Ref != "reg/base/debian:v1" || im.Origins[0].Images != 5 || im.Origins[1].Ref != "reg/ops/tool:v1" {
		t.Fatalf("origins = %+v", im.Origins)
	}
	// Teams: payments (worker, in use), web (site, cron), and no team.
	if len(im.Groups) != 3 || im.Groups[0].Value != "payments" || im.Groups[1].Value != "" || im.Groups[1].Images != 3 {
		t.Fatalf("groups = %+v", im.Groups)
	}

	// A filter narrows the images; grouping by repo works too.
	im, err = svc.Impact(ctx, ImpactOptions{CVE: "CVE-A", Filter: `stale.last_pulled < 30d`, GroupBy: "repo", InUseDays: 7})
	if err != nil || im.Total != 1 || im.Images[0].Repo != "reg/pay/worker" || im.Groups[0].Value != "reg/pay/worker" || im.InUse != 1 {
		t.Fatalf("filtered = %+v, %v", im, err)
	}
	if got := im.Images[0].Row(); len(got) != len(ImpactColumns) || got[4] != 2 || got[8] != true {
		t.Fatalf("row = %v", got)
	}

	// A CVE nothing has gives an empty answer, not an error.
	if im, err := svc.Impact(ctx, ImpactOptions{CVE: "CVE-NONE"}); err != nil || im.Total != 0 || len(im.Groups) != 0 {
		t.Fatalf("unknown CVE = %+v, %v", im, err)
	}
	for _, bad := range []ImpactOptions{{}, {Ref: "x", CVE: "y"}, {CVE: "CVE-A", Filter: "nope = 1"}, {CVE: "CVE-A", GroupBy: "tag"}} {
		if _, err := svc.Impact(ctx, bad); !IsInputError(err) {
			t.Errorf("Impact(%+v) = %v, want an input error", bad, err)
		}
	}
	if _, err := svc.Impact(ctx, ImpactOptions{Ref: "reg/missing:v1"}); err == nil {
		t.Fatal("unknown ref resolved")
	}
}

func TestIsVulnID(t *testing.T) {
	t.Parallel()
	for s, want := range map[string]bool{
		"CVE-2024-3094": true, "cve-2024-3094": true, "GHSA-xxxx-yyyy": true, "ALAS2-2024-123": true, "USN-1-1": true,
		"reg.io/base/go:1.22": false, "debian": false, "sha256:abc": false, "CVE-2024/x:1": false,
	} {
		if IsVulnID(s) != want {
			t.Errorf("IsVulnID(%q) = %v", s, !want)
		}
	}
}
