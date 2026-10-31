package crawl

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rossedman/strata/internal/fixture"
	"github.com/rossedman/strata/internal/model"
	"github.com/rossedman/strata/internal/testreg"
)

// scanLister lists scans with each manifest of the repos in listed, as
// enhanced scanning does, and returns findings for any digest, as
// DescribeImageScanFindings does.
type scanLister struct {
	*fakeLister
	listed map[string]model.Scan
	fail   map[string]bool

	mu    sync.Mutex
	asked []string
}

func (l *scanLister) Images(ctx context.Context, repo string) ([]Listed, error) {
	out, err := l.fakeLister.Images(ctx, repo)
	for i := range out {
		if sc, ok := l.listed[repo]; ok {
			out[i].Scan = &sc
		}
	}
	return out, err
}

func (l *scanLister) Scan(_ context.Context, repo, digest string) (*model.Scan, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.asked = append(l.asked, digest)
	if l.fail[repo] {
		return nil, errors.New("throttled")
	}
	sc := model.Scan{Source: "test", Status: model.ScanComplete, CompletedAt: fixture.Epoch, Critical: 1, High: 1,
		Findings: []model.Finding{
			{ID: "CVE-2024-3094", Package: "xz-utils", Version: "5.6.0", Severity: "critical", FixedIn: "5.6.1"},
			{ID: "CVE-2023-1", Package: "zlib", Version: "1.2", Severity: "high"},
		}}
	if sc2, ok := l.listed[repo]; ok {
		sc.CompletedAt, sc.Critical, sc.High = sc2.CompletedAt, sc2.Critical, sc2.High
	}
	return &sc, nil
}

// calls returns and resets the digests Scan was asked for.
func (l *scanLister) calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.asked)
	l.asked = nil
	return n
}

func TestCrawlScans(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := testreg.Start(t)
	s := fixture.Open(t, nil)
	now := fixture.Epoch
	l := &scanLister{
		fakeLister: &fakeLister{r: r},
		listed: map[string]model.Scan{
			"payments/api": {Source: "test", Status: model.ScanComplete, CompletedAt: now, Critical: 1, High: 1},
		},
		fail: map[string]bool{"payments/worker": true},
	}
	o := opts(r)
	o.Now = func() time.Time { return now }
	o.Lister = l

	// Without Findings, only the listing's scans are stored.
	stats, err := Crawl(ctx, s, o)
	if err != nil {
		t.Fatal(err)
	}
	if l.calls() != 0 || stats.Scans == 0 || stats.Scans != int64(count(t, s, `SELECT count(*) FROM scans`)) || stats.Findings != 0 {
		t.Fatalf("calls without findings, stats = %+v", stats)
	}
	if n := count(t, s, `SELECT count(*) FROM image_view WHERE vuln_critical = 1 AND vuln_fixable IS NULL`); n == 0 {
		t.Fatalf("scanned images = %d", n)
	}

	o.Findings = true
	stats, err = Crawl(ctx, s, o)
	if err != nil {
		t.Fatal(err)
	}
	// Every indexed manifest listed is asked for (the fake lists no
	// platform images of an index, and indexes are not scanned); the
	// worker's fail and keep what the listing said, which is nothing.
	total := count(t, s, `SELECT count(*) FROM images WHERE index_digest IS NULL`)
	asked := l.calls()
	workers := count(t, s, `SELECT count(*) FROM images WHERE repo LIKE '%payments/worker'`)
	if asked != total || workers == 0 || stats.Findings != int64(asked-workers) {
		t.Fatalf("asked %d of %d images, stats = %+v", asked, total, stats)
	}
	if n := count(t, s, `SELECT count(*) FROM scans WHERE findings`); n != asked-workers {
		t.Fatalf("scans with findings = %d", n)
	}
	if n := count(t, s, `SELECT count(*) FROM image_view WHERE vuln_fixable = 1`); n != total-workers {
		t.Fatalf("images with a fixable vulnerability = %d", n)
	}

	// Nothing changed and nothing is a week old: only the failures are
	// asked again.
	if _, err := Crawl(ctx, s, o); err != nil {
		t.Fatal(err)
	}
	if n := l.calls(); n != workers {
		t.Fatalf("unchanged re-crawl asked for %d scans, want %d", n, workers)
	}

	// A week on, scans the listing does not report are fetched again;
	// the listed one is not, since its counts did not change.
	later := now.Add(8 * 24 * time.Hour)
	o.Now = func() time.Time { return later }
	if _, err := Crawl(ctx, s, o); err != nil {
		t.Fatal(err)
	}
	api := count(t, s, `SELECT count(*) FROM images WHERE repo LIKE '%payments/api'`)
	if n := l.calls(); n != asked-api {
		t.Fatalf("week-later re-crawl asked for %d scans, want %d", n, asked-api)
	}

	// A rescan with new counts is fetched again, and its findings replace
	// the old ones.
	l.listed["payments/api"] = model.Scan{Source: "test", Status: model.ScanComplete, CompletedAt: later, Critical: 2, High: 1}
	if _, err := Crawl(ctx, s, o); err != nil {
		t.Fatal(err)
	}
	if n := l.calls(); n != api+workers {
		t.Fatalf("rescan asked for %d scans, want %d", n, api+workers)
	}
	if n := count(t, s, `SELECT count(*) FROM vulnerabilities v JOIN images i ON i.digest = v.digest WHERE i.repo LIKE '%payments/api'`); n != 2*api {
		t.Fatalf("findings of api = %d", n)
	}
	if n := count(t, s, `SELECT count(*) FROM image_view WHERE vuln_critical = 2 AND vuln_fixable = 1`); n != api {
		t.Fatalf("rescanned images = %d", n)
	}

	// A listing that reports a new scan when findings are off clears
	// the stale findings flag, so vuln.id stops matching old findings.
	l.listed["payments/api"] = model.Scan{Source: "test", Status: model.ScanComplete, CompletedAt: later.Add(time.Hour), Critical: 3}
	o.Findings = false
	if _, err := Crawl(ctx, s, o); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM image_view WHERE vuln_critical = 3 AND vuln_fixable IS NULL`); n != api {
		t.Fatalf("images whose findings went stale = %d", n)
	}
}
