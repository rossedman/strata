package service

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"

	"github.com/rossedman/strata/internal/crawl"
	"github.com/rossedman/strata/internal/fixture"
	"github.com/rossedman/strata/internal/testreg"
)

// slow delays every registry request, so a crawl lasts long enough to
// refresh in the middle.
type slow struct{ base http.RoundTripper }

// RoundTrip waits, then forwards the request.
func (s slow) RoundTrip(r *http.Request) (*http.Response, error) {
	time.Sleep(2 * time.Millisecond)
	return s.base.RoundTrip(r)
}

func TestIndexStatusAndRefreshDuring(t *testing.T) {
	t.Parallel()
	r := testreg.StartSynthetic(t, testreg.Shape{Repos: 12, Tags: 4, Bases: 2})
	svc := New(fixture.Open(t, nil))
	svc.RefreshEvery = 20 * time.Millisecond
	if st := svc.Status(); st.Running || st.StartedAt != nil || st.RefreshEverySeconds != 0 {
		t.Fatalf("status before indexing = %+v", st)
	}
	var calls, sawRunning atomic.Int64
	opts := crawl.Options{Registry: r.Host, Insecure: true, Workers: 2, Keychain: authn.NewMultiKeychain(), Transport: slow{http.DefaultTransport},
		Progress: func(crawl.Stats) {
			calls.Add(1)
			if svc.Status().Running {
				sawRunning.Add(1)
			}
		}}
	res, err := svc.Index(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	st := svc.Status()
	if st.Running || st.FinishedAt == nil || st.RefreshedAt == nil || st.Registry != r.Host || st.Error != "" {
		t.Fatalf("status after indexing = %+v", st)
	}
	if st.Crawl.ReposTotal != int64(len(r.RepoNames)) || st.Crawl.Images != res.Crawl.Images {
		t.Fatalf("crawl totals = %+v", st.Crawl)
	}
	// One report once repos are listed, then one per repo, all while
	// running; the caller's own Progress still gets them.
	if calls.Load() != int64(len(r.RepoNames))+1 || sawRunning.Load() != calls.Load() {
		t.Fatalf("progress calls = %d, while running = %d", calls.Load(), sawRunning.Load())
	}

	// A failed index keeps the error in the status.
	opts.Registry = "127.0.0.1:1"
	if _, err := svc.Index(context.Background(), opts); err == nil {
		t.Fatal("indexing an unreachable registry should fail")
	}
	if st := svc.Status(); st.Running || st.Error == "" {
		t.Fatalf("status after a failure = %+v", st)
	}
}

func TestRefreshDuringSkipsIdleTicks(t *testing.T) {
	t.Parallel()
	svc := New(fixture.Open(t, fixture.Org(50)))
	svc.RefreshEvery = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.refreshDuring(ctx); close(done) }()
	// No new images: ticks pass without refreshing.
	time.Sleep(30 * time.Millisecond)
	if svc.Status().RefreshedAt != nil {
		t.Fatal("refreshed without new images")
	}
	svc.update(func(st *IndexStatus) { st.Crawl.Images = 10 })
	deadline := time.Now().Add(5 * time.Second)
	for svc.Status().RefreshedAt == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if svc.Status().RefreshedAt == nil {
		t.Fatal("new images did not trigger a refresh")
	}
}
