// Package acr lists Azure Container Registry through its /acr/v1 API
// instead of the distribution API. One manifests page covers up to 1,000
// manifests of a repository with their tags and push times, so tags need
// no request each.
package acr

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/rossedman/strata/internal/crawl"
	"github.com/rossedman/strata/internal/regapi"
)

// IsHost reports whether host is an Azure Container Registry login server.
func IsHost(host string) bool {
	return strings.HasSuffix(host, ".azurecr.io") || strings.HasSuffix(host, ".azurecr.cn") || strings.HasSuffix(host, ".azurecr.us")
}

// Registry is one Azure Container Registry. It is a crawl.Lister.
type Registry struct {
	reg  name.Registry
	base string
	auth authn.Authenticator
	rt   http.RoundTripper
	api  *regapi.Client
}

// New returns a client for an ACR login server. Credentials come from the
// keychain: `az acr login` stores a token in the Docker config, and a
// service principal or token can be used as a username and password.
// Both are exchanged for registry tokens scoped to each call.
func New(host string, insecure bool, kc authn.Keychain, rt http.RoundTripper, userAgent string) (*Registry, error) {
	var opts []name.Option
	if insecure {
		opts = append(opts, name.Insecure)
	}
	reg, err := name.NewRegistry(host, opts...)
	if err != nil {
		return nil, err
	}
	if kc == nil {
		kc = authn.DefaultKeychain
	}
	auth, err := kc.Resolve(reg)
	if err != nil {
		return nil, fmt.Errorf("credentials for %s: %w", host, err)
	}
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &Registry{reg: reg, base: regapi.Scheme(insecure) + "://" + host, auth: auth, rt: rt, api: &regapi.Client{UserAgent: userAgent}}, nil
}

// Calls returns how many ACR API calls the client has made.
func (r *Registry) Calls() int64 { return r.api.Calls() }

// list follows a paged /acr/v1 listing with a token for scope, decoding
// each page into a T.
func list[T any](ctx context.Context, r *Registry, scope, u string, each func(T)) error {
	t, err := transport.NewWithContext(ctx, r.reg, r.auth, r.rt, []string{scope})
	if err != nil {
		return fmt.Errorf("authenticate to %s: %w", r.reg, err)
	}
	hc := &http.Client{Transport: t, Timeout: 2 * time.Minute}
	for u != "" {
		var p T
		if u, err = r.api.GetVia(ctx, hc, u, &p); err != nil {
			return err
		}
		each(p)
	}
	return nil
}

// Repos lists every repository in the registry.
func (r *Registry) Repos(ctx context.Context) ([]string, error) {
	var out []string
	err := list(ctx, r, "registry:catalog:*", r.base+"/acr/v1/_catalog?n=1000", func(p struct {
		Repositories []string `json:"repositories"`
	}) {
		out = append(out, p.Repositories...)
	})
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	return out, nil
}

// manifestsPage is one page of a repository's manifests.
type manifestsPage struct {
	Manifests []struct {
		Digest      string    `json:"digest"`
		MediaType   string    `json:"mediaType"`
		CreatedTime time.Time `json:"createdTime"`
		Tags        []string  `json:"tags"`
	} `json:"manifests"`
}

// Images lists every manifest in a repository with its tags and push
// time. ACR reports no pull times.
func (r *Registry) Images(ctx context.Context, repo string) ([]crawl.Listed, error) {
	var out []crawl.Listed
	err := list(ctx, r, "repository:"+repo+":metadata_read", r.base+"/acr/v1/"+escapePath(repo)+"/_manifests?n=1000", func(p manifestsPage) {
		for _, m := range p.Manifests {
			out = append(out, crawl.Listed{Digest: m.Digest, MediaType: m.MediaType, Tags: m.Tags, PushedAt: m.CreatedTime})
		}
	})
	if err != nil {
		return nil, fmt.Errorf("list manifests in %s: %w", repo, err)
	}
	return out, nil
}

// escapePath escapes each segment of a repository name.
func escapePath(repo string) string {
	parts := strings.Split(repo, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
