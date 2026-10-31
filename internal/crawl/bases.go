package crawl

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

// baseRetry is how long a base that could not be fetched is left before
// it is tried again.
const baseRetry = 7 * 24 * time.Hour

// fetchBases fetches, by digest, every base that provenance names and the
// index lacks, so lineage can walk images up to bases in other registries
// and to base versions whose tag has since moved. A base that cannot be
// fetched, because it was deleted or the credentials cannot reach it, is
// recorded with the reason and tried again a week later; it never fails
// the crawl.
func (c *crawler) fetchBases(ctx context.Context) error {
	now := c.opts.Now().UTC()
	missing, err := c.st.MissingBases(ctx, now.Add(-baseRetry))
	if err != nil {
		return err
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(c.opts.Workers)
	for _, b := range missing {
		g.Go(func() error {
			host, _, _ := strings.Cut(b.Ref, "/")
			ferr := c.crawlExternal(gctx, b.FetchRef(), !c.indexedHost(host))
			if gctx.Err() != nil {
				return gctx.Err()
			}
			text := ""
			if ferr != nil {
				text = ferr.Error()
				atomic.AddInt64(&c.stats.BasesMissing, 1)
				c.opts.Logger.Debug("base not fetched", "base", b.FetchRef(), "err", ferr)
			} else {
				atomic.AddInt64(&c.stats.BasesFetched, 1)
			}
			return c.st.RecordBaseFetch(gctx, b, text, now)
		})
	}
	return g.Wait()
}

// indexedHost reports whether host is one of the registries the index
// crawls.
func (c *crawler) indexedHost(host string) bool {
	if host == c.opts.Registry {
		return true
	}
	for _, h := range c.opts.IndexedHosts {
		if h == host {
			return true
		}
	}
	return false
}
