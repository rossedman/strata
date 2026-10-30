// Package crawl walks a registry over the OCI Distribution API and writes
// what it finds to the index: repos, then tags, then manifests and image
// configs. It never pulls layer blobs.
//
// Every fetch is keyed by digest. A manifest already in the index is not
// fetched again, and a config shared by many images is fetched once, so a
// re-crawl costs a tag list and a HEAD per tag.
package crawl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"github.com/rossedman/strata/internal/model"
	"github.com/rossedman/strata/internal/store"
)

// Options configures a crawl.
type Options struct {
	// Registry is the registry host, such as reg.io or localhost:5000.
	Registry string
	// Repos limits the crawl to these repositories (without the host).
	// Empty means every repo the catalog lists.
	Repos []string
	// External are full refs of bases outside the registry, such as
	// docker.io/library/debian:bookworm-slim, fetched so roots resolve.
	External []string
	// Workers bounds concurrent requests.
	Workers int
	// RequestsPerSecond limits the request rate; zero means unlimited.
	RequestsPerSecond float64
	// Insecure allows plain HTTP.
	Insecure bool
	// Referrers fetches SBOMs, signatures and attestations per image.
	Referrers bool
	// EnvAllowlist names environment variables whose values are safe to
	// store.
	EnvAllowlist []string
	// Filter narrows which repos, tags, platforms and images are fetched.
	Filter Filter
	// Lister lists repos and images through the registry's own API, such
	// as ECR's, instead of the catalog, tag list and one request per tag.
	// It also reports push and pull times.
	Lister Lister
	// Redactions are regular expressions scrubbed from history commands.
	Redactions []string
	// BatchSize is how many images are written per transaction.
	BatchSize int
	// Keychain resolves registry credentials.
	Keychain authn.Keychain
	// Transport overrides the HTTP transport, for tests.
	Transport http.RoundTripper
	// UserAgent identifies strata to the registry.
	UserAgent string
	// Logger receives progress.
	Logger *slog.Logger
	// Now returns the current time, for tests.
	Now func() time.Time
}

// Stats summarize a crawl.
type Stats struct {
	Repos     int64 `json:"repos"`
	Skipped   int64 `json:"repos_resumed"`
	Tags      int64 `json:"tags"`
	Images    int64 `json:"new_images"`
	TagMoves  int64 `json:"tag_moves"`
	Requests  int64 `json:"requests"`
	Referrers int64 `json:"referrers"`
	// Filtered counts what the filter skipped before fetching it.
	ReposFiltered  int64 `json:"repos_filtered"`
	TagsFiltered   int64 `json:"tags_filtered"`
	ImagesFiltered int64 `json:"images_filtered"`
}

// RequestsPerImage is the registry requests the crawl made for each new
// image, or zero when it found none.
func (s Stats) RequestsPerImage() float64 {
	if s.Images == 0 {
		return 0
	}
	return float64(s.Requests) / float64(s.Images)
}

// Lister lists a registry through its own API.
type Lister interface {
	// Repos lists every repository, named without the registry host.
	Repos(ctx context.Context) ([]string, error)
	// Images lists every manifest in a repository, tagged or not.
	Images(ctx context.Context, repo string) ([]Listed, error)
}

// Listed is one manifest a Lister reports.
type Listed struct {
	Digest    string
	MediaType string
	Tags      []string
	// PushedAt and LastPulled are zero when unknown.
	PushedAt   time.Time
	LastPulled time.Time
	// Subject is the manifest an artifact, such as an SBOM or a
	// signature, is attached to; ArtifactType says what it is.
	Subject      string
	ArtifactType string
}

// crawler holds the state shared by one crawl.
type crawler struct {
	opts    Options
	st      *store.Store
	remote  []remote.Option
	nameOpt []name.Option
	red     *Redactor
	match   *matcher
	stats   Stats
	sem     chan struct{}
	// configs caches parsed configs by digest so each is fetched once.
	configs sync.Map
	// writeMu serializes writes to the index.
	writeMu sync.Mutex
}

// Crawl indexes a registry. A crawl that stops part way is resumed by the
// next call: repos it finished are skipped.
func Crawl(ctx context.Context, st *store.Store, opts Options) (Stats, error) {
	if opts.Workers < 1 {
		opts.Workers = 16
	}
	if opts.BatchSize < 1 {
		opts.BatchSize = 500
	}
	if opts.Keychain == nil {
		opts.Keychain = authn.DefaultKeychain
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Redactions == nil {
		opts.Redactions = DefaultRedactions
	}
	red, err := NewRedactor(opts.Redactions)
	if err != nil {
		return Stats{}, fmt.Errorf("redaction pattern: %w", err)
	}
	match, err := opts.Filter.compile()
	if err != nil {
		return Stats{}, err
	}
	if match.pushedSince > 0 && opts.Lister == nil && opts.Registry != "" {
		return Stats{}, fmt.Errorf("pushed-since needs a registry that reports push times, such as ECR")
	}
	c := &crawler{opts: opts, st: st, red: red, match: match, sem: make(chan struct{}, opts.Workers)}
	base := opts.Transport
	if base == nil {
		base = remote.DefaultTransport
	}
	limit := rate.Inf
	if opts.RequestsPerSecond > 0 {
		limit = rate.Limit(opts.RequestsPerSecond)
	}
	rt := &countingTransport{base: base, limiter: rate.NewLimiter(limit, opts.Workers), n: &c.stats.Requests}
	c.remote = []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(opts.Keychain),
		remote.WithTransport(rt),
		remote.WithUserAgent(opts.UserAgent),
	}
	// One puller for the whole crawl, so the registry handshake and token
	// exchange happen once per repo instead of before every request.
	puller, err := remote.NewPuller(c.remote...)
	if err != nil {
		return Stats{}, fmt.Errorf("registry client: %w", err)
	}
	c.remote = append(c.remote, remote.Reuse(puller))
	if opts.Insecure {
		c.nameOpt = append(c.nameOpt, name.Insecure)
	}
	if opts.Registry != "" {
		if err := c.crawlRegistry(ctx); err != nil {
			return c.stats, err
		}
	}
	for _, ref := range opts.External {
		if err := c.crawlExternal(ctx, ref); err != nil {
			return c.stats, err
		}
	}
	opts.Logger.Info("crawl finished", "registry", opts.Registry, "new_images", c.stats.Images,
		"requests", c.stats.Requests, "requests_per_image", fmt.Sprintf("%.1f", c.stats.RequestsPerImage()),
		"repos_filtered", c.stats.ReposFiltered, "tags_filtered", c.stats.TagsFiltered, "images_filtered", c.stats.ImagesFiltered)
	return c.stats, nil
}

// crawlRegistry lists the registry's repos and crawls each one not
// already finished by this run.
func (c *crawler) crawlRegistry(ctx context.Context) error {
	run, done, err := c.st.StartCrawl(ctx, c.opts.Registry)
	if err != nil {
		return err
	}
	repos := c.opts.Repos
	switch {
	case len(repos) > 0:
	case c.opts.Lister != nil:
		if repos, err = c.opts.Lister.Repos(ctx); err != nil {
			return fmt.Errorf("list repos: %w", err)
		}
	default:
		if repos, err = c.catalog(ctx); err != nil {
			return err
		}
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(c.opts.Workers)
	for _, r := range repos {
		repo, err := name.NewRepository(c.opts.Registry+"/"+r, c.nameOpt...)
		if err != nil {
			return fmt.Errorf("repo %q: %w", r, err)
		}
		full := RepoName(repo)
		if !c.match.repo(r) {
			atomic.AddInt64(&c.stats.ReposFiltered, 1)
			continue
		}
		if done[full] {
			atomic.AddInt64(&c.stats.Skipped, 1)
			continue
		}
		g.Go(func() error {
			if err := c.crawlRepo(gctx, repo); err != nil {
				return fmt.Errorf("%s: %w", full, err)
			}
			atomic.AddInt64(&c.stats.Repos, 1)
			return c.st.CheckpointRepo(gctx, run, full)
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	return c.st.FinishCrawl(ctx, run)
}

// catalog pages through the registry's _catalog endpoint.
func (c *crawler) catalog(ctx context.Context) ([]string, error) {
	reg, err := name.NewRegistry(c.opts.Registry, c.nameOpt...)
	if err != nil {
		return nil, fmt.Errorf("registry %q: %w", c.opts.Registry, err)
	}
	var out []string
	last := ""
	for {
		page, err := remote.CatalogPage(reg, last, 1000, c.remote...)
		if err != nil {
			return nil, fmt.Errorf("list repos (set repos in the config if the registry has no catalog): %w", err)
		}
		out = append(out, page...)
		if len(page) < 1000 || ctx.Err() != nil {
			break
		}
		last = page[len(page)-1]
	}
	c.opts.Logger.Info("catalog listed", "registry", c.opts.Registry, "repos", len(out))
	return out, ctx.Err()
}

// RepoName returns a repository's full name with the registry host, using
// docker.io rather than index.docker.io for Docker Hub.
func RepoName(r name.Repository) string {
	host := r.RegistryStr()
	if host == name.DefaultRegistry {
		host = "docker.io"
	}
	return host + "/" + r.RepositoryStr()
}

// tagResult is what crawling one tag produced.
type tagResult struct {
	tag    model.Tag
	images []*model.Image
	index  []model.IndexEntry
	refs   []model.Referrer
}

// crawlRepo lists a repo's tags, records every tag that moved, and fetches
// manifests and configs the index has not seen.
func (c *crawler) crawlRepo(ctx context.Context, repo name.Repository) error {
	full := RepoName(repo)
	now := c.opts.Now().UTC()
	batch := &store.Batch{}
	var tags []string
	var listed map[string]*Listed
	if c.opts.Lister != nil {
		var err error
		if tags, listed, err = c.listImages(ctx, repo, batch); err != nil {
			return err
		}
	} else {
		var err error
		if tags, err = c.listTags(ctx, repo); err != nil {
			return err
		}
	}
	known, err := c.st.KnownDigests(ctx, full)
	if err != nil {
		return err
	}
	prev, err := c.st.Tags(ctx, full)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	seenDigest := map[string]bool{}
	flush := func(force bool) error {
		if !force && len(batch.Images) < c.opts.BatchSize {
			return nil
		}
		b := batch
		batch = &store.Batch{}
		c.writeMu.Lock()
		defer c.writeMu.Unlock()
		return c.st.Write(ctx, b)
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(2 * c.opts.Workers)
	for _, tag := range tags {
		if !c.match.tag(tag) {
			atomic.AddInt64(&c.stats.TagsFiltered, 1)
			continue
		}
		var desc *Listed
		if listed != nil {
			desc = listed[tag]
			if !known[desc.Digest] && !c.match.pushed(desc.PushedAt, now) {
				atomic.AddInt64(&c.stats.ImagesFiltered, 1)
				continue
			}
		}
		atomic.AddInt64(&c.stats.Tags, 1)
		g.Go(func() error {
			res, err := c.crawlTag(gctx, repo, tag, desc, func(d string) bool {
				mu.Lock()
				defer mu.Unlock()
				if known[d] || seenDigest[d] {
					return true
				}
				seenDigest[d] = true
				return false
			})
			if err != nil {
				return err
			}
			if res == nil {
				return nil
			}
			mu.Lock()
			defer mu.Unlock()
			if prev[tag] != res.tag.Digest {
				batch.Tags = append(batch.Tags, res.tag)
				batch.Events = append(batch.Events, model.TagEvent{Repo: full, Tag: tag, SeenAt: now, Digest: res.tag.Digest, Source: "crawl"})
				atomic.AddInt64(&c.stats.TagMoves, 1)
			}
			batch.Images = append(batch.Images, res.images...)
			batch.Index = append(batch.Index, res.index...)
			batch.Referrers = append(batch.Referrers, res.refs...)
			atomic.AddInt64(&c.stats.Images, int64(len(res.images)))
			atomic.AddInt64(&c.stats.Referrers, int64(len(res.refs)))
			return flush(false)
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	current := map[string]bool{}
	for _, t := range tags {
		current[t] = true
	}
	// Tags the filter skipped are left as the index last saw them; only
	// tags the registry no longer lists are deleted.
	for t := range prev {
		if !current[t] {
			batch.DeleteTags = append(batch.DeleteTags, model.Tag{Repo: full, Tag: t})
			batch.Events = append(batch.Events, model.TagEvent{Repo: full, Tag: t, SeenAt: now, Source: "crawl"})
			atomic.AddInt64(&c.stats.TagMoves, 1)
		}
	}
	c.opts.Logger.Debug("repo crawled", "repo", full, "tags", len(tags))
	return flush(true)
}

// listImages lists a repo through the Lister. It returns the sorted tags
// and the manifest each points at, and adds push times and attached
// artifacts to the batch, so no per-tag requests are needed.
func (c *crawler) listImages(ctx context.Context, repo name.Repository, batch *store.Batch) ([]string, map[string]*Listed, error) {
	full := RepoName(repo)
	images, err := c.opts.Lister.Images(ctx, repo.RepositoryStr())
	if err != nil {
		return nil, nil, fmt.Errorf("list images: %w", err)
	}
	byTag := map[string]*Listed{}
	var tags []string
	for i := range images {
		l := &images[i]
		batch.Pushes = append(batch.Pushes, model.Push{Repo: full, Digest: l.Digest, PushedAt: l.PushedAt, LastPulled: l.LastPulled})
		if l.Subject != "" && c.opts.Referrers {
			batch.Referrers = append(batch.Referrers, model.Referrer{Subject: l.Subject, Digest: l.Digest, ArtifactType: l.ArtifactType, Kind: model.ClassifyArtifact(l.ArtifactType)})
			atomic.AddInt64(&c.stats.Referrers, 1)
		}
		for _, t := range l.Tags {
			byTag[t] = l
			tags = append(tags, t)
		}
	}
	sort.Strings(tags)
	return tags, byTag, nil
}

// listTags lists a repo's tags, sorted.
func (c *crawler) listTags(ctx context.Context, repo name.Repository) ([]string, error) {
	c.acquire(ctx)
	defer c.release()
	tags, err := remote.List(repo, c.remote...)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	sort.Strings(tags)
	return tags, nil
}

// acquire takes a request slot, so total concurrency stays at Workers.
// Slots are never nested, so this cannot deadlock.
func (c *crawler) acquire(_ context.Context) {
	c.sem <- struct{}{}
}

// release frees a request slot.
func (c *crawler) release() { <-c.sem }

// crawlTag resolves a tag to a digest and, if seen reports the digest is
// new, fetches its manifests and configs. A tag the Lister already
// resolved (listed is non-nil) needs no request to resolve. Cosign
// signature tags become referrers of the image they sign.
func (c *crawler) crawlTag(ctx context.Context, repo name.Repository, tag string, listed *Listed, seen func(string) bool) (*tagResult, error) {
	desc, err := c.resolve(ctx, repo.Tag(tag), listed)
	if err != nil || desc == nil {
		return nil, err
	}
	digest := desc.Digest.String()
	res := &tagResult{tag: model.Tag{Repo: RepoName(repo), Tag: tag, Digest: digest, UpdatedAt: c.opts.Now().UTC()}}
	if r, ok := cosignReferrer(tag, digest); ok {
		res.refs = append(res.refs, r)
		return res, nil
	}
	if seen(digest) {
		return res, nil
	}
	if err := c.fetch(ctx, repo, desc.Digest, desc.MediaType, res); err != nil {
		return nil, err
	}
	return res, nil
}

// resolve returns the descriptor a tag points at: from the listing when
// there is one, else with a HEAD request. It returns nil for a tag
// deleted since it was listed.
func (c *crawler) resolve(ctx context.Context, ref name.Tag, listed *Listed) (*v1.Descriptor, error) {
	if listed != nil {
		h, err := v1.NewHash(listed.Digest)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", ref, err)
		}
		return &v1.Descriptor{Digest: h, MediaType: types.MediaType(listed.MediaType)}, nil
	}
	c.acquire(ctx)
	desc, err := remote.Head(ref, c.remote...)
	c.release()
	if err != nil {
		var terr *transport.Error
		if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
			return nil, nil // deleted between list and head
		}
		return nil, fmt.Errorf("resolve %s: %w", ref, err)
	}
	return desc, nil
}

// fetch reads a manifest or index by digest and every image it holds.
func (c *crawler) fetch(ctx context.Context, repo name.Repository, digest v1.Hash, mt types.MediaType, res *tagResult) error {
	ref := repo.Digest(digest.String())
	if mt.IsIndex() {
		c.acquire(ctx)
		idx, err := remote.Index(ref, c.remote...)
		var im *v1.IndexManifest
		if err == nil {
			im, err = idx.IndexManifest()
		}
		c.release()
		if err != nil {
			return fmt.Errorf("read index %s: %w", ref, err)
		}
		for _, m := range im.Manifests {
			if !m.MediaType.IsImage() || isAttestationManifest(m) {
				continue
			}
			if m.Platform != nil && !c.match.platform(model.Platform{OS: m.Platform.OS, Arch: m.Platform.Architecture, Variant: m.Platform.Variant}) {
				atomic.AddInt64(&c.stats.ImagesFiltered, 1)
				continue
			}
			img, err := c.image(ctx, repo, m.Digest)
			if err != nil {
				return err
			}
			if img == nil {
				continue
			}
			img.IndexDigest = digest.String()
			res.images = append(res.images, img)
			p := img.Platform
			if m.Platform != nil {
				p = model.Platform{OS: m.Platform.OS, Arch: m.Platform.Architecture, Variant: m.Platform.Variant}
			}
			res.index = append(res.index, model.IndexEntry{IndexDigest: digest.String(), Digest: img.Digest, Platform: p})
			res.refs = append(res.refs, c.referrers(ctx, repo, m.Digest)...)
		}
		return nil
	}
	img, err := c.image(ctx, repo, digest)
	if err != nil || img == nil {
		return err
	}
	res.images = append(res.images, img)
	res.refs = append(res.refs, c.referrers(ctx, repo, digest)...)
	return nil
}

// isAttestationManifest reports whether an index entry is a BuildKit
// attestation manifest rather than a runnable platform.
func isAttestationManifest(d v1.Descriptor) bool {
	if d.Annotations["vnd.docker.reference.type"] == "attestation-manifest" {
		return true
	}
	return d.Platform != nil && d.Platform.OS == "unknown"
}

// image fetches one platform manifest and its config. It returns nil for
// artifacts that are not runnable images.
func (c *crawler) image(ctx context.Context, repo name.Repository, digest v1.Hash) (*model.Image, error) {
	ref := repo.Digest(digest.String())
	c.acquire(ctx)
	img, err := remote.Image(ref, c.remote...)
	var m *v1.Manifest
	if err == nil {
		m, err = img.Manifest()
	}
	c.release()
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", ref, err)
	}
	if !isImageConfig(string(m.Config.MediaType)) {
		return nil, nil
	}
	cfg, err := c.config(ctx, img, m.Config.Digest)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", m.Config.Digest, err)
	}
	return toImage(digest.String(), RepoName(repo), m, cfg, c.opts.EnvAllowlist, c.red)
}

// config returns an image's config, fetching each digest once per crawl.
func (c *crawler) config(ctx context.Context, img v1.Image, digest v1.Hash) (*v1.ConfigFile, error) {
	if v, ok := c.configs.Load(digest); ok {
		return v.(*v1.ConfigFile), nil
	}
	c.acquire(ctx)
	cfg, err := img.ConfigFile()
	c.release()
	if err != nil {
		return nil, err
	}
	c.configs.Store(digest, cfg)
	return cfg, nil
}

// referrers lists artifacts attached to an image through the OCI
// referrers API, unless a Lister already reported them. Registries
// without the API return nothing rather than fail the crawl.
func (c *crawler) referrers(ctx context.Context, repo name.Repository, digest v1.Hash) []model.Referrer {
	// A Lister reports attached artifacts with the listing.
	if !c.opts.Referrers || c.opts.Lister != nil {
		return nil
	}
	c.acquire(ctx)
	idx, err := remote.Referrers(repo.Digest(digest.String()), c.remote...)
	var im *v1.IndexManifest
	if err == nil {
		im, err = idx.IndexManifest()
	}
	c.release()
	if err != nil {
		c.opts.Logger.Debug("referrers unavailable", "image", digest, "err", err)
		return nil
	}
	var out []model.Referrer
	for _, m := range im.Manifests {
		at := m.ArtifactType
		if at == "" {
			at = string(m.MediaType)
		}
		out = append(out, model.Referrer{Subject: digest.String(), Digest: m.Digest.String(), ArtifactType: at, Kind: model.ClassifyArtifact(at)})
	}
	return out
}

// crawlExternal fetches one base from outside the registry and marks it
// external, so images built on it resolve to a named root.
func (c *crawler) crawlExternal(ctx context.Context, s string) error {
	ref, err := name.ParseReference(s, c.nameOpt...)
	if err != nil {
		return fmt.Errorf("external base %q: %w", s, err)
	}
	c.acquire(ctx)
	desc, err := remote.Head(ref, c.remote...)
	c.release()
	if err != nil {
		return fmt.Errorf("resolve external base %s: %w", s, err)
	}
	repo := ref.Context()
	res := &tagResult{}
	known, err := c.st.KnownImage(ctx, desc.Digest.String())
	if err != nil {
		return err
	}
	if !known {
		if err := c.fetch(ctx, repo, desc.Digest, desc.MediaType, res); err != nil {
			return err
		}
	}
	for _, img := range res.images {
		img.External = true
	}
	b := &store.Batch{Images: res.images, Index: res.index, Referrers: res.refs}
	if t, ok := ref.(name.Tag); ok {
		full := RepoName(repo)
		prev, err := c.st.Tags(ctx, full)
		if err != nil {
			return err
		}
		now := c.opts.Now().UTC()
		if prev[t.TagStr()] != desc.Digest.String() {
			b.Tags = append(b.Tags, model.Tag{Repo: full, Tag: t.TagStr(), Digest: desc.Digest.String(), UpdatedAt: now})
			b.Events = append(b.Events, model.TagEvent{Repo: full, Tag: t.TagStr(), SeenAt: now, Digest: desc.Digest.String(), Source: "crawl"})
			atomic.AddInt64(&c.stats.TagMoves, 1)
		}
	}
	atomic.AddInt64(&c.stats.Images, int64(len(res.images)))
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.st.Write(ctx, b)
}

// countingTransport rate-limits and counts registry requests.
type countingTransport struct {
	base    http.RoundTripper
	limiter *rate.Limiter
	n       *int64
}

// RoundTrip waits for the rate limiter, then forwards the request.
func (t *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := t.limiter.Wait(r.Context()); err != nil {
		return nil, err
	}
	atomic.AddInt64(t.n, 1)
	return t.base.RoundTrip(r)
}
