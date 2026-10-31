// Package regapi is the HTTP client the registry API listers share: JSON
// GETs with authentication, paging through Link headers, and a count of
// calls for the crawl's request totals.
package regapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sync/atomic"
)

// Client calls one API.
type Client struct {
	// HTTP sends requests; nil means http.DefaultClient.
	HTTP *http.Client
	// Auth, when set, adds credentials to each request.
	Auth func(*http.Request) error
	// UserAgent identifies strata.
	UserAgent string

	calls atomic.Int64
}

// Calls returns how many API calls the client has made.
func (c *Client) Calls() int64 { return c.calls.Load() }

// linkNext matches the next page in a Link header.
var linkNext = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="?next"?`)

// Get fetches u and decodes its JSON body into out. It returns the next
// page's URL from the Link header, resolved against u, or "".
func (c *Client) Get(ctx context.Context, u string, out any) (string, error) {
	return c.GetVia(ctx, c.HTTP, u, out)
}

// GetVia is Get over another HTTP client, such as one whose transport
// carries a token for a narrower scope.
func (c *Client) GetVia(ctx context.Context, hc *http.Client, u string, out any) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Auth != nil {
		if err := c.Auth(req); err != nil {
			return "", err
		}
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	c.calls.Add(1)
	res, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return "", &StatusError{URL: u, Code: res.StatusCode, Body: string(body)}
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return "", fmt.Errorf("decode %s: %w", u, err)
	}
	m := linkNext.FindStringSubmatch(res.Header.Get("Link"))
	if m == nil {
		return "", nil
	}
	base, err := url.Parse(u)
	if err != nil {
		return "", err
	}
	next, err := base.Parse(m[1])
	if err != nil {
		return "", fmt.Errorf("next page link %q: %w", m[1], err)
	}
	return next.String(), nil
}

// StatusError is a response other than 200 OK.
type StatusError struct {
	URL  string
	Code int
	Body string
}

// Error describes the failed call.
func (e *StatusError) Error() string {
	return fmt.Sprintf("GET %s: %d %s", e.URL, e.Code, e.Body)
}

// Scheme returns the URL scheme for a registry host.
func Scheme(insecure bool) string {
	if insecure {
		return "http"
	}
	return "https"
}
