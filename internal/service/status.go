package service

import (
	"context"
	"sync"
	"time"

	"github.com/rossedman/strata/internal/crawl"
)

// IndexStatus is the state of the running index, or of the last one.
type IndexStatus struct {
	// Running is whether a crawl is in progress.
	Running bool `json:"running"`
	// Registry is the registry being or last crawled.
	Registry   string     `json:"registry,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// RefreshedAt is when lineage, staleness and the views were last
	// recomputed; results in the UI are as of then. It is unset until the
	// first refresh of the index finishes, and lineage, staleness and the
	// registry map are empty until then.
	RefreshedAt *time.Time `json:"refreshed_at,omitempty"`
	// Refreshing is whether a refresh is running now.
	Refreshing bool `json:"refreshing"`
	// RefreshEverySeconds is how often views refresh while a crawl runs;
	// zero means only when it finishes.
	RefreshEverySeconds int64 `json:"refresh_every_seconds"`
	// Crawl holds the running totals.
	Crawl crawl.Stats `json:"crawl"`
	// LastRefresh says what the last refresh rewrote and how long it took.
	LastRefresh *RefreshStats `json:"last_refresh,omitempty"`
	// Error is why the last index failed.
	Error string `json:"error,omitempty"`
	// RefreshError is why the last refresh failed, so the UI can say so
	// instead of waiting for views that will not come; a refresh that
	// succeeds clears it.
	RefreshError string `json:"refresh_error,omitempty"`
}

// status is the service's index state, shared with request handlers.
type status struct {
	mu sync.Mutex
	IndexStatus
}

// Status returns the state of the running or last index.
func (s *Service) Status() IndexStatus {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	out := s.st.IndexStatus
	out.RefreshEverySeconds = int64(s.RefreshEvery / time.Second)
	return out
}

// update changes the status under its lock.
func (s *Service) update(f func(*IndexStatus)) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	f(&s.st.IndexStatus)
}

// firstRefreshAfter is how soon into a crawl the first refresh runs, when
// RefreshEvery is longer, so a long first crawl shows lineage and
// staleness early instead of after a whole interval.
var firstRefreshAfter = time.Minute

// refreshDuring refreshes the views while a crawl runs, when the crawl has
// found new images since the last refresh, so a long first crawl fills the
// UI as it goes: first after firstRefreshAfter, then every RefreshEvery.
// A refresh of a large index takes minutes, so the next one waits at
// least twice as long as the last took, keeping refreshes from crowding
// out the crawl. It returns when ctx ends.
func (s *Service) refreshDuring(ctx context.Context) {
	t := time.NewTicker(s.RefreshEvery)
	defer t.Stop()
	first := time.NewTimer(min(firstRefreshAfter, s.RefreshEvery))
	defer first.Stop()
	var seen int64
	var next time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
		case <-t.C:
		}
		n := s.Status().Crawl.Images
		if n == seen || time.Now().Before(next) {
			continue
		}
		seen = n
		start := time.Now()
		if err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
			s.logger().Warn("refresh during crawl failed", "err", err)
		}
		next = time.Now().Add(time.Since(start))
	}
}
