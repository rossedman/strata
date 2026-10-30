package crawl

import (
	"context"
	"sync"

	"github.com/rossedman/strata/internal/store"
)

// writer merges the batches of many repos into few transactions. Every
// write has a fixed cost per table, and most repos hold a handful of
// images, so writing each repo on its own made the index the bottleneck
// of a crawl. A repo is checkpointed in the transaction that stores the
// last of its rows, so a resumed crawl re-crawls any repo whose rows were
// still pending when the crawl stopped.
type writer struct {
	st *store.Store
	// size is how many images a merged batch holds before it is written.
	size int
	run  int64

	mu      sync.Mutex
	pending *store.Batch
	images  map[string]bool
	index   map[[2]string]bool
	refs    map[[2]string]bool
}

// newWriter returns a writer for one crawl run.
func newWriter(st *store.Store, run int64, size int) *writer {
	w := &writer{st: st, run: run, size: size}
	w.reset()
	return w
}

// reset starts an empty merged batch.
func (w *writer) reset() {
	w.pending = &store.Batch{Run: w.run}
	w.images = map[string]bool{}
	w.index = map[[2]string]bool{}
	w.refs = map[[2]string]bool{}
}

// add merges a batch, marks repo finished when it is set, and writes once
// the merged batch is full. Images found in two repos are stored once.
func (w *writer) add(ctx context.Context, b *store.Batch, repo string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.pending
	for _, img := range b.Images {
		if !w.images[img.Digest] {
			w.images[img.Digest] = true
			p.Images = append(p.Images, img)
		}
	}
	for _, e := range b.Index {
		if k := [2]string{e.IndexDigest, e.Digest}; !w.index[k] {
			w.index[k] = true
			p.Index = append(p.Index, e)
		}
	}
	for _, r := range b.Referrers {
		if k := [2]string{r.Subject, r.Digest}; !w.refs[k] {
			w.refs[k] = true
			p.Referrers = append(p.Referrers, r)
		}
	}
	p.Tags = append(p.Tags, b.Tags...)
	p.DeleteTags = append(p.DeleteTags, b.DeleteTags...)
	p.Events = append(p.Events, b.Events...)
	p.Pushes = append(p.Pushes, b.Pushes...)
	if repo != "" {
		p.Done = append(p.Done, repo)
	}
	// Tags and pushes are small rows, so they get a larger allowance.
	if len(p.Images) < w.size && len(p.Tags)+len(p.Pushes)+len(p.Events) < 20*w.size {
		return nil
	}
	return w.flushLocked(ctx)
}

// flush writes whatever is pending.
func (w *writer) flush(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked(ctx)
}

// flushLocked writes the merged batch; the caller holds mu.
func (w *writer) flushLocked(ctx context.Context) error {
	b := w.pending
	w.reset()
	return w.st.Write(ctx, b)
}
