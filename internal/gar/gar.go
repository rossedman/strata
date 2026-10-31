// Package gar lists Google Artifact Registry Docker repositories through
// the Artifact Registry API instead of the distribution API. One
// dockerImages page covers up to 1,000 manifests of a repository with
// their tags and upload times, so tags need no request each. Scans come
// from Artifact Analysis (the Container Analysis API).
package gar

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rossedman/strata/internal/crawl"
	"github.com/rossedman/strata/internal/model"
	"github.com/rossedman/strata/internal/regapi"
)

// hostPattern matches Artifact Registry Docker hosts, such as
// us-central1-docker.pkg.dev.
var hostPattern = regexp.MustCompile(`^([a-z0-9-]+)-docker\.pkg\.dev$`)

// ParseHost returns the location of an Artifact Registry host.
func ParseHost(host string) (location string, ok bool) {
	m := hostPattern.FindStringSubmatch(host)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// Default API endpoints.
const (
	ArtifactRegistryAPI  = "https://artifactregistry.googleapis.com/v1"
	ContainerAnalysisAPI = "https://containeranalysis.googleapis.com/v1"
)

// Options configure a client.
type Options struct {
	// Projects are the Google Cloud projects whose repositories are
	// listed. Repos can also be seeded as project/repository/image.
	Projects []string
	// Token returns an OAuth access token for the APIs.
	Token func() (string, error)
	// RegistryAPI and AnalysisAPI override the endpoints, for tests.
	RegistryAPI, AnalysisAPI string
	Transport                http.RoundTripper
	UserAgent                string
}

// Registry is one Artifact Registry location. It is a crawl.Lister and
// a crawl.ScanFetcher.
type Registry struct {
	host, location string
	opts           Options
	api            *regapi.Client

	mu sync.Mutex
	// listed holds images listed with their repository and not yet
	// asked for, by strata repo name; loaded marks the repositories
	// already listed.
	listed map[string][]crawl.Listed
	loaded map[string]bool
}

// New returns a client for an Artifact Registry host.
func New(host string, opts Options) (*Registry, error) {
	location, ok := ParseHost(host)
	if !ok {
		return nil, fmt.Errorf("%s is not an Artifact Registry host, such as us-docker.pkg.dev", host)
	}
	if opts.RegistryAPI == "" {
		opts.RegistryAPI = ArtifactRegistryAPI
	}
	if opts.AnalysisAPI == "" {
		opts.AnalysisAPI = ContainerAnalysisAPI
	}
	r := &Registry{host: host, location: location, opts: opts, listed: map[string][]crawl.Listed{}, loaded: map[string]bool{}}
	r.api = &regapi.Client{HTTP: &http.Client{Transport: opts.Transport, Timeout: 2 * time.Minute}, UserAgent: opts.UserAgent, Auth: func(req *http.Request) error {
		if opts.Token == nil {
			return nil
		}
		tok, err := opts.Token()
		if err != nil {
			return fmt.Errorf("get a Google access token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		return nil
	}}
	return r, nil
}

// Calls returns how many API calls the client has made.
func (r *Registry) Calls() int64 { return r.api.Calls() }

// withToken adds a page token to a list URL.
func withToken(u, token string) string {
	if token == "" {
		return u
	}
	return u + "&pageToken=" + url.QueryEscape(token)
}

// Repos lists the image paths, as project/repository/image, of every
// Docker repository in the configured projects at this location. Listing
// a repository lists all its images, which Images then hands out.
func (r *Registry) Repos(ctx context.Context) ([]string, error) {
	var out []string
	for _, project := range r.opts.Projects {
		parent := "projects/" + project + "/locations/" + r.location
		var repos []string
		for tok, first := "", true; first || tok != ""; first = false {
			var p struct {
				Repositories []struct {
					Name   string `json:"name"`
					Format string `json:"format"`
				} `json:"repositories"`
				NextPageToken string `json:"nextPageToken"`
			}
			if _, err := r.api.Get(ctx, withToken(r.opts.RegistryAPI+"/"+parent+"/repositories?pageSize=1000", tok), &p); err != nil {
				return nil, fmt.Errorf("list repositories of %s: %w", parent, err)
			}
			for _, repo := range p.Repositories {
				if repo.Format == "DOCKER" {
					repos = append(repos, repo.Name[strings.LastIndex(repo.Name, "/")+1:])
				}
			}
			tok = p.NextPageToken
		}
		for _, repo := range repos {
			images, err := r.load(ctx, project, repo)
			if err != nil {
				return nil, err
			}
			out = append(out, images...)
		}
	}
	return out, nil
}

// load lists every image in a repository once and holds them for Images,
// returning their paths.
func (r *Registry) load(ctx context.Context, project, repo string) ([]string, error) {
	key := project + "/" + repo
	r.mu.Lock()
	done := r.loaded[key]
	r.mu.Unlock()
	if done {
		return nil, nil
	}
	parent := "projects/" + project + "/locations/" + r.location + "/repositories/" + repo
	byPath := map[string][]crawl.Listed{}
	var paths []string
	for tok, first := "", true; first || tok != ""; first = false {
		var p struct {
			DockerImages []struct {
				URI        string    `json:"uri"`
				Tags       []string  `json:"tags"`
				MediaType  string    `json:"mediaType"`
				UploadTime time.Time `json:"uploadTime"`
			} `json:"dockerImages"`
			NextPageToken string `json:"nextPageToken"`
		}
		if _, err := r.api.Get(ctx, withToken(r.opts.RegistryAPI+"/"+parent+"/dockerImages?pageSize=1000", tok), &p); err != nil {
			return nil, fmt.Errorf("list images in %s: %w", parent, err)
		}
		for _, img := range p.DockerImages {
			ref, digest, ok := strings.Cut(img.URI, "@")
			path := strings.TrimPrefix(ref, r.host+"/")
			if !ok || path == ref {
				continue
			}
			if byPath[path] == nil {
				paths = append(paths, path)
			}
			byPath[path] = append(byPath[path], crawl.Listed{Digest: digest, MediaType: img.MediaType, Tags: img.Tags, PushedAt: img.UploadTime})
		}
		tok = p.NextPageToken
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loaded[key] = true
	for p, l := range byPath {
		r.listed[p] = l
	}
	return paths, nil
}

// Images lists every manifest of an image path, project/repository/image,
// with its tags and upload time.
func (r *Registry) Images(ctx context.Context, path string) ([]crawl.Listed, error) {
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("artifact registry image %q is not project/repository/image", path)
	}
	if _, err := r.load(ctx, parts[0], parts[1]); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.listed[path]
	if !ok {
		return nil, fmt.Errorf("artifact registry image %s not found", path)
	}
	// A repo is crawled once; drop its listing to free memory.
	delete(r.listed, path)
	return l, nil
}

// occurrence is an Artifact Analysis occurrence for one image.
type occurrence struct {
	Kind          string    `json:"kind"`
	UpdateTime    time.Time `json:"updateTime"`
	Vulnerability *struct {
		EffectiveSeverity string `json:"effectiveSeverity"`
		Severity          string `json:"severity"`
		ShortDescription  string `json:"shortDescription"`
		PackageIssue      []struct {
			AffectedPackage string `json:"affectedPackage"`
			AffectedVersion struct {
				FullName string `json:"fullName"`
			} `json:"affectedVersion"`
			FixedVersion struct {
				FullName string `json:"fullName"`
				Kind     string `json:"kind"`
			} `json:"fixedVersion"`
		} `json:"packageIssue"`
		RelatedURLs []struct {
			URL string `json:"url"`
		} `json:"relatedUrls"`
	} `json:"vulnerability"`
	Discovery *struct {
		AnalysisStatus    string    `json:"analysisStatus"`
		LastScanTime      time.Time `json:"lastScanTime"`
		AnalysisCompleted *struct{} `json:"analysisCompleted"`
	} `json:"discovery"`
}

// Scan returns an image's vulnerability scan from Artifact Analysis: the
// discovery occurrence says whether and when it was scanned, and each
// vulnerability occurrence is one finding per affected package.
func (r *Registry) Scan(ctx context.Context, path, digest string) (*model.Scan, error) {
	project, _, _ := strings.Cut(path, "/")
	resource := "https://" + r.host + "/" + path + "@" + digest
	filter := url.QueryEscape(`resourceUrl="` + resource + `" AND (kind="VULNERABILITY" OR kind="DISCOVERY")`)
	sc := &model.Scan{Digest: digest, Source: "artifact-analysis", Status: model.ScanNone, HasFindings: true}
	for tok, first := "", true; first || tok != ""; first = false {
		var p struct {
			Occurrences   []occurrence `json:"occurrences"`
			NextPageToken string       `json:"nextPageToken"`
		}
		u := r.opts.AnalysisAPI + "/projects/" + project + "/occurrences?pageSize=1000&filter=" + filter
		if _, err := r.api.Get(ctx, withToken(u, tok), &p); err != nil {
			return nil, fmt.Errorf("list scan occurrences of %s@%s: %w", path, digest, err)
		}
		for _, o := range p.Occurrences {
			switch {
			case o.Discovery != nil:
				switch o.Discovery.AnalysisStatus {
				case "FINISHED_SUCCESS", "COMPLETE":
					sc.Status = model.ScanComplete
				default:
					sc.Status = strings.ToLower(o.Discovery.AnalysisStatus)
				}
				sc.CompletedAt = o.Discovery.LastScanTime
			case o.Vulnerability != nil:
				v := o.Vulnerability
				sev := v.EffectiveSeverity
				if sev == "" || sev == "SEVERITY_UNSPECIFIED" {
					sev = v.Severity
				}
				sc.Count(sev, 1)
				f := model.Finding{ID: v.ShortDescription, Severity: model.NormalizeSeverity(sev)}
				if len(v.RelatedURLs) > 0 {
					f.URI = v.RelatedURLs[0].URL
				}
				if len(v.PackageIssue) == 0 {
					sc.Findings = append(sc.Findings, f)
				}
				for _, pi := range v.PackageIssue {
					pf := f
					pf.Package, pf.Version = pi.AffectedPackage, pi.AffectedVersion.FullName
					if pi.FixedVersion.Kind != "MAXIMUM" {
						pf.FixedIn = pi.FixedVersion.FullName
					}
					sc.Findings = append(sc.Findings, pf)
				}
			}
		}
		tok = p.NextPageToken
	}
	return sc, nil
}
