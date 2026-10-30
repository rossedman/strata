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
	// recomputed; results in the UI are as of then.
	RefreshedAt *time.Time `json:"refreshed_at,omitempty"`
	// RefreshEverySeconds is how often views refresh while a crawl runs;
	// zero means only when it finishes.
	RefreshEverySeconds int64 `json:"refresh_every_seconds"`
	// Crawl holds the running totals.
	Crawl crawl.Stats `json:"crawl"`
	// Error is why the last index failed.
	Error string `json:"error,omitempty"`
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

// refreshDuring refreshes the views every RefreshEvery while a crawl
// runs, when the crawl has found new images since the last refresh, so a
// long first crawl fills the UI as it goes. A refresh of a large index
// takes minutes, so the next one waits at least twice as long as the last
// took, keeping refreshes from crowding out the crawl. It returns when
// ctx ends.
func (s *Service) refreshDuring(ctx context.Context) {
	t := time.NewTicker(s.RefreshEvery)
	defer t.Stop()
	var seen int64
	var next time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n := s.Status().Crawl.Images
			if n == seen || time.Now().Before(next) {
				continue
			}
			seen = n
			start := time.Now()
			if err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
				s.logger().Warn("refresh during crawl failed", "err", err)
			}
			took := time.Since(start)
			next = time.Now().Add(took)
		}
	}
}
