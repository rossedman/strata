// Package harbor lists a Harbor registry through Harbor's v2 API instead
// of the distribution API. One artifacts page covers up to 100 manifests
// with their tags, push and pull times, vulnerability scan summary and
// attached signatures and SBOMs, so tags need no request each.
package harbor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"

	"github.com/rossedman/strata/internal/crawl"
	"github.com/rossedman/strata/internal/model"
	"github.com/rossedman/strata/internal/regapi"
)

// Registry is one Harbor instance. It is a crawl.Lister.
type Registry struct {
	api  *regapi.Client
	base string
}

// New returns a client for the Harbor at host. Credentials, such as a
// robot account, come from the keychain as a username and password.
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
	return &Registry{
		base: regapi.Scheme(insecure) + "://" + host + "/api/v2.0",
		api: &regapi.Client{HTTP: &http.Client{Transport: rt, Timeout: 2 * time.Minute}, UserAgent: userAgent, Auth: func(r *http.Request) error {
			cfg, err := auth.Authorization()
			if err != nil {
				return err
			}
			if cfg.Username != "" || cfg.Password != "" {
				r.SetBasicAuth(cfg.Username, cfg.Password)
			}
			return nil
		}},
	}, nil
}

// Calls returns how many Harbor API calls the client has made.
func (r *Registry) Calls() int64 { return r.api.Calls() }

// Repos lists every repository the credentials can see, as
// project/repository.
func (r *Registry) Repos(ctx context.Context) ([]string, error) {
	var out []string
	for u := r.base + "/repositories?page_size=100"; u != ""; {
		var page []struct {
			Name string `json:"name"`
		}
		next, err := r.api.Get(ctx, u, &page)
		if err != nil {
			return nil, fmt.Errorf("list repositories: %w", err)
		}
		for _, p := range page {
			out = append(out, p.Name)
		}
		u = next
	}
	return out, nil
}

// artifact is one entry of Harbor's artifact list.
type artifact struct {
	Digest    string    `json:"digest"`
	MediaType string    `json:"manifest_media_type"`
	PushTime  time.Time `json:"push_time"`
	PullTime  time.Time `json:"pull_time"`
	Tags      []struct {
		Name string `json:"name"`
	} `json:"tags"`
	ScanOverview map[string]scanOverview `json:"scan_overview"`
	Accessories  []struct {
		Digest  string `json:"digest"`
		Type    string `json:"type"`
		Subject string `json:"subject_artifact_digest"`
	} `json:"accessories"`
}

// scanOverview is a scan report summary, keyed by report media type.
type scanOverview struct {
	Status  string    `json:"scan_status"`
	EndTime time.Time `json:"end_time"`
	Summary struct {
		Fixable int            `json:"fixable"`
		Summary map[string]int `json:"summary"`
	} `json:"summary"`
}

// artifactsURL returns the artifacts endpoint of a repo named
// project/repository; Harbor wants slashes in the repository name
// escaped twice.
func (r *Registry) artifactsURL(repo string) (string, error) {
	project, rest, ok := strings.Cut(repo, "/")
	if !ok || rest == "" {
		return "", fmt.Errorf("harbor repo %q is not project/repository", repo)
	}
	return r.base + "/projects/" + url.PathEscape(project) + "/repositories/" + url.PathEscape(url.PathEscape(rest)) + "/artifacts", nil
}

// Images lists every artifact in a repository with its tags, push and
// pull times and scan summary. Signatures and SBOMs Harbor keeps as
// accessories are listed as artifacts attached to their subject.
func (r *Registry) Images(ctx context.Context, repo string) ([]crawl.Listed, error) {
	base, err := r.artifactsURL(repo)
	if err != nil {
		return nil, err
	}
	var out []crawl.Listed
	for u := base + "?page_size=100&with_tag=true&with_scan_overview=true&with_accessory=true"; u != ""; {
		var page []artifact
		next, err := r.api.Get(ctx, u, &page)
		if err != nil {
			return nil, fmt.Errorf("list artifacts in %s: %w", repo, err)
		}
		for _, a := range page {
			l := crawl.Listed{Digest: a.Digest, MediaType: a.MediaType, PushedAt: known(a.PushTime), LastPulled: known(a.PullTime), Scan: scanOf(a.ScanOverview)}
			for _, t := range a.Tags {
				l.Tags = append(l.Tags, t.Name)
			}
			out = append(out, l)
			for _, acc := range a.Accessories {
				subject := acc.Subject
				if subject == "" {
					subject = a.Digest
				}
				out = append(out, crawl.Listed{Digest: acc.Digest, Subject: subject, ArtifactType: acc.Type, PushedAt: known(a.PushTime)})
			}
		}
		u = next
	}
	return out, nil
}

// known returns t, or zero for Harbor's zero time, 0001-01-01, which it
// reports for an artifact never pulled.
func known(t time.Time) time.Time {
	if t.Year() <= 1 {
		return time.Time{}
	}
	return t
}

// scanOf converts Harbor's scan overview, which holds one report per
// scanner report type, to a scan; nil when the artifact was not scanned.
func scanOf(overview map[string]scanOverview) *model.Scan {
	for _, o := range overview {
		sc := &model.Scan{Source: "harbor", Status: strings.ToLower(o.Status), CompletedAt: known(o.EndTime)}
		if o.Status == "Success" {
			sc.Status = model.ScanComplete
		}
		for sev, n := range o.Summary.Summary {
			sc.Count(sev, n)
		}
		return sc
	}
	return nil
}

// Scan returns an artifact's scan with every vulnerability it found.
func (r *Registry) Scan(ctx context.Context, repo, digest string) (*model.Scan, error) {
	base, err := r.artifactsURL(repo)
	if err != nil {
		return nil, err
	}
	var a artifact
	if _, err := r.api.Get(ctx, base+"/"+url.PathEscape(digest)+"?with_scan_overview=true", &a); err != nil {
		return nil, fmt.Errorf("read artifact %s@%s: %w", repo, digest, err)
	}
	sc := scanOf(a.ScanOverview)
	if sc == nil {
		return &model.Scan{Digest: digest, Source: "harbor", Status: model.ScanNone, HasFindings: true}, nil
	}
	sc.Digest, sc.HasFindings = digest, true
	if sc.Status != model.ScanComplete {
		return sc, nil
	}
	var reports map[string]struct {
		Vulnerabilities []struct {
			ID         string   `json:"id"`
			Package    string   `json:"package"`
			Version    string   `json:"version"`
			FixVersion string   `json:"fix_version"`
			Severity   string   `json:"severity"`
			Links      []string `json:"links"`
		} `json:"vulnerabilities"`
	}
	if _, err := r.api.Get(ctx, base+"/"+url.PathEscape(digest)+"/additions/vulnerabilities", &reports); err != nil {
		return nil, fmt.Errorf("read vulnerabilities of %s@%s: %w", repo, digest, err)
	}
	for _, rep := range reports {
		for _, v := range rep.Vulnerabilities {
			f := model.Finding{ID: v.ID, Package: v.Package, Version: v.Version, Severity: model.NormalizeSeverity(v.Severity), FixedIn: v.FixVersion}
			if len(v.Links) > 0 {
				f.URI = v.Links[0]
			}
			sc.Findings = append(sc.Findings, f)
		}
		break
	}
	return sc, nil
}
