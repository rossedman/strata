package crawl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"golang.org/x/sync/errgroup"

	"github.com/rossedman/strata/internal/model"
	"github.com/rossedman/strata/internal/store"
)

// maxProvenance bounds a provenance document; BuildKit's are a few KB, or
// a few hundred KB in max mode.
const maxProvenance = 8 << 20

// provenance reads the SLSA provenance BuildKit attaches to an image as
// an attestation manifest, and returns the images the build pulled, such
// as its FROM images. A multi-stage build lists every stage's image;
// lineage keeps the one whose layers the image starts with.
func (c *crawler) provenance(ctx context.Context, repo name.Repository, att v1.Descriptor) ([]model.BaseRef, error) {
	subject := att.Annotations["vnd.docker.reference.digest"]
	if subject == "" {
		return nil, nil
	}
	m, err := c.manifest(ctx, repo.Digest(att.Digest.String()))
	if err != nil {
		return nil, fmt.Errorf("read attestation %s: %w", att.Digest, err)
	}
	var out []model.BaseRef
	for _, l := range m.Layers {
		if !strings.Contains(l.Annotations["in-toto.io/predicate-type"], "slsa.dev/provenance") {
			continue
		}
		b, err := c.blob(ctx, repo, l.Digest)
		if err != nil {
			return nil, fmt.Errorf("read provenance %s: %w", l.Digest, err)
		}
		for _, r := range parseProvenance(b) {
			r.Digest = subject
			out = append(out, r)
		}
	}
	return out, nil
}

// blob fetches a small blob, such as an in-toto statement.
func (c *crawler) blob(ctx context.Context, repo name.Repository, digest v1.Hash) ([]byte, error) {
	c.acquire(ctx)
	defer c.release()
	l, err := remote.Layer(repo.Digest(digest.String()), c.remote...)
	if err != nil {
		return nil, err
	}
	rc, err := l.Compressed()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(io.LimitReader(rc, maxProvenance))
}

// material is an input SLSA provenance lists: materials in v0.2,
// resolvedDependencies in v1.
type material struct {
	URI    string            `json:"uri"`
	Digest map[string]string `json:"digest"`
}

// parseProvenance returns the container images an in-toto statement's
// SLSA provenance lists as build inputs. The Dockerfile frontend image,
// which BuildKit also lists, is left out.
func parseProvenance(b []byte) []model.BaseRef {
	var st struct {
		Predicate struct {
			Materials       []material `json:"materials"`
			BuildDefinition struct {
				ResolvedDependencies []material `json:"resolvedDependencies"`
			} `json:"buildDefinition"`
		} `json:"predicate"`
	}
	if json.Unmarshal(b, &st) != nil {
		return nil
	}
	var out []model.BaseRef
	seen := map[string]bool{}
	for _, m := range append(st.Predicate.Materials, st.Predicate.BuildDefinition.ResolvedDependencies...) {
		ref, ok := dockerPURL(m.URI)
		if !ok || strings.Contains(ref, "/docker/dockerfile") {
			continue
		}
		d := ""
		if h := m.Digest["sha256"]; h != "" {
			d = "sha256:" + h
		}
		if seen[ref+d] {
			continue
		}
		seen[ref+d] = true
		out = append(out, model.BaseRef{Ref: ref, BaseDigest: d})
	}
	return out
}

// dockerPURL turns a package URL for a container image, as BuildKit
// writes it (pkg:docker/node@20?platform=linux%2Famd64 or
// pkg:docker/reg.io/base/node@20), into a full repo:tag ref.
func dockerPURL(uri string) (string, bool) {
	rest, ok := strings.CutPrefix(uri, "pkg:docker/")
	if !ok {
		return "", false
	}
	rest, query, _ := strings.Cut(rest, "?")
	path, version, _ := strings.Cut(rest, "@")
	path, err := url.PathUnescape(path)
	if err != nil || path == "" {
		return "", false
	}
	if q, err := url.ParseQuery(query); err == nil && q.Get("repository_url") != "" {
		path = q.Get("repository_url")
	}
	version, _ = url.PathUnescape(version)
	tagged := version != "" && !strings.HasPrefix(version, "sha256:")
	ref := path
	if tagged {
		ref += ":" + version
	}
	// Normalize Docker Hub names the way image references do.
	r, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		return "", false
	}
	repo := RepoName(r.Context())
	if t, ok := r.(name.Tag); ok && tagged {
		return repo + ":" + t.TagStr(), true
	}
	return repo, true
}

// backfillPage is how many unchecked indexes a backfill reads and writes
// at a time, so a stopped crawl keeps what it finished.
const backfillPage = 500

// backfillProvenance reads the provenance of indexes indexed before
// strata read provenance: the index manifest and its attestation, not the
// images, which are already indexed. Each index is marked checked with
// its bases in one write, so the work happens once and a stopped crawl
// resumes. An index that cannot be read is left unchecked and tried by
// the next crawl, unless the registry no longer has it.
func (c *crawler) backfillProvenance(ctx context.Context) error {
	failed := map[string]bool{}
	for {
		todo, err := c.st.UncheckedIndexes(ctx, c.registryHost(), backfillPage+len(failed))
		if err != nil {
			return err
		}
		var page []store.UncheckedIndex
		for _, u := range todo {
			if !failed[u.Digest] {
				page = append(page, u)
			}
		}
		if len(page) == 0 {
			return nil
		}
		var mu sync.Mutex
		batch := &store.Batch{}
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(c.opts.Workers)
		for _, u := range page {
			g.Go(func() error {
				bases, gone, err := c.indexProvenance(gctx, u)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case gctx.Err() != nil:
					return gctx.Err()
				case err != nil && !gone:
					failed[u.Digest] = true
					c.opts.Logger.Debug("provenance backfill failed", "index", u.Digest, "repo", u.Repo, "err", err)
					return nil
				}
				batch.BaseRefs = append(batch.BaseRefs, bases...)
				batch.ProvenanceChecked = append(batch.ProvenanceChecked, u.Digest)
				atomic.AddInt64(&c.stats.ProvenanceBackfilled, 1)
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return err
		}
		if err := c.st.Write(ctx, batch); err != nil {
			return err
		}
	}
}

// indexProvenance reads one index's attestations. gone reports that the
// registry no longer has the index, so there is nothing to retry.
func (c *crawler) indexProvenance(ctx context.Context, u store.UncheckedIndex) (bases []model.BaseRef, gone bool, err error) {
	repo, err := name.NewRepository(u.Repo, c.nameOpt...)
	if err != nil {
		return nil, true, err
	}
	h, err := v1.NewHash(u.Digest)
	if err != nil {
		return nil, true, err
	}
	im, err := c.indexManifest(ctx, repo.Digest(h.String()))
	if err != nil {
		var terr *transport.Error
		return nil, errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound, err
	}
	for _, m := range im.Manifests {
		if !isAttestationManifest(m) {
			continue
		}
		b, err := c.provenance(ctx, repo, m)
		if err != nil {
			return nil, false, err
		}
		bases = append(bases, b...)
	}
	return bases, false, nil
}

// registryHost returns the crawled registry's host as repo names start
// with it: docker.io for Docker Hub.
func (c *crawler) registryHost() string {
	r, err := name.NewRegistry(c.opts.Registry, c.nameOpt...)
	if err != nil {
		return c.opts.Registry
	}
	if h := r.RegistryStr(); h != name.DefaultRegistry {
		return h
	}
	return "docker.io"
}
