// Package ecr lists Amazon ECR registries through the ECR API instead of
// the distribution API. One DescribeImages page covers up to 1,000
// manifests with their tags, push time and last pull time, so a re-crawl
// costs one call per thousand images rather than one request per tag. It
// also signs registry requests with the AWS credential chain, so no
// Docker credential helper is needed.
package ecr

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/google/go-containerregistry/pkg/authn"

	"github.com/rossedman/strata/internal/crawl"
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
			})
		}
	}
	return out, nil
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
