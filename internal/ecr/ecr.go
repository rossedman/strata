// Package ecr lists Amazon ECR registries through the ECR API instead of
// the distribution API. One DescribeImages page covers up to 1,000
// manifests with their tags, push time, last pull time and scan counts by
// severity, so a re-crawl
// costs one call per thousand images rather than one request per tag, and
// BatchGetImage fetches up to 100 manifests per call, so a first crawl
// fetches only image configs one by one. It also signs registry requests
// with the AWS credential chain, so no Docker credential helper is needed.
package ecr

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/google/go-containerregistry/pkg/authn"

	"github.com/rossedman/strata/internal/crawl"
	"github.com/rossedman/strata/internal/model"
)

// hostPattern matches ECR registry hosts: account.dkr.ecr[-fips].region
// .amazonaws.com, with .com.cn in China.
var hostPattern = regexp.MustCompile(`^([0-9]{12})\.dkr\.ecr(-fips)?\.([a-z0-9-]+)\.amazonaws\.com(\.cn)?$`)

// ParseHost returns the account and region of an ECR registry host.
func ParseHost(host string) (account, region string, ok bool) {
	m := hostPattern.FindStringSubmatch(host)
	if m == nil {
		return "", "", false
	}
	return m[1], m[3], true
}

// API is the part of the ECR client strata uses.
type API interface {
	ecr.DescribeRepositoriesAPIClient
	ecr.DescribeImagesAPIClient
	ecr.DescribeImageScanFindingsAPIClient
	BatchGetImage(ctx context.Context, in *ecr.BatchGetImageInput, opts ...func(*ecr.Options)) (*ecr.BatchGetImageOutput, error)
	GetAuthorizationToken(ctx context.Context, in *ecr.GetAuthorizationTokenInput, opts ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error)
}

// Registry is one ECR registry. It is a crawl.Lister and an
// authn.Keychain.
type Registry struct {
	host    string
	account string
	api     API
	// ctx bounds token requests, which the keychain interface makes
	// without a context.
	ctx context.Context
	now func() time.Time

	mu      sync.Mutex
	auth    authn.AuthConfig
	expires time.Time

	// calls counts ECR API calls, for the crawl's request totals.
	calls atomic.Int64
}

// Calls returns how many ECR API calls the client has made.
func (r *Registry) Calls() int64 { return r.calls.Load() }

// manifestTypes are the manifest media types BatchGetImage may return.
// Naming them all keeps ECR from converting a manifest to another type,
// which would change its digest.
var manifestTypes = []string{
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
}

// Manifests fetches the manifests of digests in one BatchGetImage call;
// ECR takes up to 100 per call. A manifest whose bytes do not hash to the
// digest asked for, or that ECR could not return, is left out, and the
// crawl fetches it the usual way.
func (r *Registry) Manifests(ctx context.Context, repo string, digests []string) (map[string][]byte, error) {
	ids := make([]types.ImageIdentifier, len(digests))
	for i, d := range digests {
		ids[i] = types.ImageIdentifier{ImageDigest: aws.String(d)}
	}
	r.calls.Add(1)
	out, err := r.api.BatchGetImage(ctx, &ecr.BatchGetImageInput{
		RegistryId:         aws.String(r.account),
		RepositoryName:     aws.String(repo),
		ImageIds:           ids,
		AcceptedMediaTypes: manifestTypes,
	})
	if err != nil {
		return nil, fmt.Errorf("batch get images in %s: %w", repo, err)
	}
	got := map[string][]byte{}
	for _, img := range out.Images {
		if img.ImageId == nil || img.ImageManifest == nil {
			continue
		}
		d, b := aws.ToString(img.ImageId.ImageDigest), []byte(aws.ToString(img.ImageManifest))
		if sum := sha256.Sum256(b); "sha256:"+hex.EncodeToString(sum[:]) != d {
			continue
		}
		got[d] = b
	}
	return got, nil
}

// New returns a client for an ECR registry host, using the default AWS
// credential chain: environment, shared config and SSO profiles, web
// identity (EKS) and instance roles. The region comes from the host.
func New(ctx context.Context, host string, opts ...func(*awsconfig.LoadOptions) error) (*Registry, error) {
	account, region, ok := ParseHost(host)
	if !ok {
		return nil, fmt.Errorf("%s is not an ECR registry host", host)
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, append([]func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return NewWithAPI(ctx, host, account, ecr.NewFromConfig(cfg)), nil
}

// NewWithAPI returns a client that calls api, for tests and custom
// clients.
func NewWithAPI(ctx context.Context, host, account string, api API) *Registry {
	return &Registry{host: host, account: account, api: api, ctx: ctx, now: time.Now}
}

// Repos lists every repository in the registry.
func (r *Registry) Repos(ctx context.Context) ([]string, error) {
	p := ecr.NewDescribeRepositoriesPaginator(r.api, &ecr.DescribeRepositoriesInput{
		RegistryId: aws.String(r.account),
		MaxResults: aws.Int32(1000),
	})
	var out []string
	for p.HasMorePages() {
		r.calls.Add(1)
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("describe repositories: %w", err)
		}
		for _, repo := range page.Repositories {
			out = append(out, aws.ToString(repo.RepositoryName))
		}
	}
	return out, nil
}

// Images lists every active manifest in a repository. Archived images
// cannot be pulled, so they are left out.
func (r *Registry) Images(ctx context.Context, repo string) ([]crawl.Listed, error) {
	p := ecr.NewDescribeImagesPaginator(r.api, &ecr.DescribeImagesInput{
		RegistryId:     aws.String(r.account),
		RepositoryName: aws.String(repo),
		MaxResults:     aws.Int32(1000),
	})
	var out []crawl.Listed
	for p.HasMorePages() {
		r.calls.Add(1)
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("describe images in %s: %w", repo, err)
		}
		for _, d := range page.ImageDetails {
			if d.ImageStatus == types.ImageStatusArchived {
				continue
			}
			out = append(out, crawl.Listed{
				Digest:       aws.ToString(d.ImageDigest),
				MediaType:    aws.ToString(d.ImageManifestMediaType),
				Tags:         d.ImageTags,
				PushedAt:     aws.ToTime(d.ImagePushedAt),
				LastPulled:   aws.ToTime(d.LastRecordedPullTime),
				Subject:      aws.ToString(d.SubjectManifestDigest),
				ArtifactType: aws.ToString(d.ArtifactMediaType),
				Scan:         scanOf(d),
			})
		}
	}
	return out, nil
}

// scanOf returns the scan summary DescribeImages reports for an image, or
// nil when it reports none, as for images under basic scanning.
func scanOf(d types.ImageDetail) *model.Scan {
	if d.ImageScanStatus == nil && d.ImageScanFindingsSummary == nil {
		return nil
	}
	sc := &model.Scan{Source: "ecr", Status: scanStatus(d.ImageScanStatus)}
	if s := d.ImageScanFindingsSummary; s != nil {
		sc.CompletedAt = aws.ToTime(s.ImageScanCompletedAt)
		for sev, n := range s.FindingSeverityCounts {
			sc.Count(sev, int(n))
		}
		if sc.Status == "" {
			sc.Status = model.ScanComplete
		}
	}
	return sc
}

// Scan returns the latest scan of an image with its findings, from basic
// or enhanced scanning. One finding is returned per vulnerable package.
// Basic scanning reports its results only here, not in DescribeImages.
// An image ECR has not scanned has status model.ScanNone.
func (r *Registry) Scan(ctx context.Context, repo, digest string) (*model.Scan, error) {
	p := ecr.NewDescribeImageScanFindingsPaginator(r.api, &ecr.DescribeImageScanFindingsInput{
		RegistryId:     aws.String(r.account),
		RepositoryName: aws.String(repo),
		ImageId:        &types.ImageIdentifier{ImageDigest: aws.String(digest)},
		MaxResults:     aws.Int32(1000),
	})
	sc := &model.Scan{Digest: digest, Source: "ecr", HasFindings: true}
	for first := true; p.HasMorePages(); first = false {
		r.calls.Add(1)
		page, err := p.NextPage(ctx)
		var none *types.ScanNotFoundException
		if errors.As(err, &none) {
			return &model.Scan{Digest: digest, Source: "ecr", Status: model.ScanNone, HasFindings: true}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("describe scan findings of %s@%s: %w", repo, digest, err)
		}
		f := page.ImageScanFindings
		if first {
			sc.Status = scanStatus(page.ImageScanStatus)
			if f != nil {
				if sc.Status == "" {
					sc.Status = model.ScanComplete
				}
				sc.CompletedAt = aws.ToTime(f.ImageScanCompletedAt)
				for sev, n := range f.FindingSeverityCounts {
					sc.Count(sev, int(n))
				}
			}
		}
		if f != nil {
			sc.Findings = append(sc.Findings, findingsOf(f)...)
		}
	}
	return sc, nil
}

// scanStatus maps ECR's scan status to strata's.
func scanStatus(st *types.ImageScanStatus) string {
	if st == nil {
		return ""
	}
	switch st.Status {
	// Enhanced scanning reports ACTIVE for an image it keeps rescanning;
	// its findings are as complete as a finished scan's.
	case types.ScanStatusComplete, types.ScanStatusActive:
		return model.ScanComplete
	}
	return strings.ToLower(string(st.Status))
}

// findingsOf converts one page of basic and enhanced findings.
func findingsOf(f *types.ImageScanFindings) []model.Finding {
	var out []model.Finding
	for _, f := range f.Findings {
		fd := model.Finding{ID: aws.ToString(f.Name), Severity: model.NormalizeSeverity(string(f.Severity)), URI: aws.ToString(f.Uri)}
		for _, a := range f.Attributes {
			switch aws.ToString(a.Key) {
			case "package_name":
				fd.Package = aws.ToString(a.Value)
			case "package_version":
				fd.Version = aws.ToString(a.Value)
			}
		}
		out = append(out, fd)
	}
	for _, f := range f.EnhancedFindings {
		d := f.PackageVulnerabilityDetails
		if d == nil {
			continue
		}
		base := model.Finding{ID: aws.ToString(d.VulnerabilityId), Severity: model.NormalizeSeverity(aws.ToString(f.Severity)), URI: aws.ToString(d.SourceUrl)}
		if len(d.VulnerablePackages) == 0 {
			out = append(out, base)
		}
		for _, vp := range d.VulnerablePackages {
			fd := base
			fd.Package, fd.Version, fd.FixedIn = aws.ToString(vp.Name), aws.ToString(vp.Version), aws.ToString(vp.FixedInVersion)
			out = append(out, fd)
		}
	}
	return out
}

// Resolve returns credentials for this registry and anonymous for every
// other host, so it composes with other keychains.
func (r *Registry) Resolve(target authn.Resource) (authn.Authenticator, error) {
	if target.RegistryStr() != r.host {
		return authn.Anonymous, nil
	}
	return authenticator{r}, nil
}

// authenticator fetches a registry token when first used and again
// shortly before it expires, so crawls longer than the token's 12 hours
// keep working.
type authenticator struct{ r *Registry }

// Authorization returns the current registry credentials.
func (a authenticator) Authorization() (*authn.AuthConfig, error) {
	return a.r.token()
}

// token returns a cached registry token, refreshing it within ten minutes
// of expiry.
func (r *Registry) token() (*authn.AuthConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.now().Before(r.expires.Add(-10 * time.Minute)) {
		cfg := r.auth
		return &cfg, nil
	}
	out, err := r.api.GetAuthorizationToken(r.ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return nil, fmt.Errorf("get ECR authorization token: %w", err)
	}
	if len(out.AuthorizationData) == 0 {
		return nil, fmt.Errorf("get ECR authorization token: no authorization data")
	}
	d := out.AuthorizationData[0]
	raw, err := base64.StdEncoding.DecodeString(aws.ToString(d.AuthorizationToken))
	if err != nil {
		return nil, fmt.Errorf("decode ECR authorization token: %w", err)
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	if !ok {
		return nil, fmt.Errorf("decode ECR authorization token: want user:password")
	}
	r.auth = authn.AuthConfig{Username: user, Password: pass}
	r.expires = aws.ToTime(d.ExpiresAt)
	cfg := r.auth
	return &cfg, nil
}
