package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/rossedman/strata/internal/testenv"
	"github.com/rossedman/strata/pkg/client"
)

var ctx = context.Background()

// validating wraps a transport and checks every response against the
// OpenAPI spec, so tests prove server, client and spec agree.
type validating struct {
	t    *testing.T
	base http.RoundTripper
}

// RoundTrip forwards a request and validates its response.
func (v validating) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := v.base.RoundTrip(r)
	if err != nil {
		return res, err
	}
	spec, err := GetSpec()
	if err != nil {
		v.t.Fatal(err)
	}
	spec.Servers = nil
	router, err := gorillamux.NewRouter(spec)
	if err != nil {
		v.t.Fatal(err)
	}
	route, params, err := router.FindRoute(r)
	if err != nil {
		v.t.Fatalf("no route for %s: %v", r.URL, err)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		v.t.Fatal(err)
	}
	_ = res.Body.Close()
	res.Body = io.NopCloser(strings.NewReader(string(body)))
	in := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: r, PathParams: params, Route: route},
		Status:                 res.StatusCode, Header: res.Header, Body: io.NopCloser(strings.NewReader(string(body))),
	}
	if err := openapi3filter.ValidateResponse(ctx, in); err != nil {
		v.t.Errorf("%s %s: response does not match the spec: %v\n%s", r.Method, r.URL, err, body)
	}
	return res, nil
}

// serve starts the API over an indexed registry and returns a validating
// client.
func serve(t *testing.T, opts Options) (*client.ClientWithResponses, *testenv.Env) {
	t.Helper()
	e := testenv.Indexed(t)
	h, err := New(e.Svc, opts)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := client.NewClientWithResponses(srv.URL, client.WithHTTPClient(&http.Client{Transport: validating{t: t, base: http.DefaultTransport}}))
	if err != nil {
		t.Fatal(err)
	}
	return c, e
}

func TestEndpoints(t *testing.T) {
	t.Parallel()
	c, e := serve(t, Options{Version: "test"})
	host := e.Reg.Host

	h, err := c.GetHealthWithResponse(ctx)
	if err != nil || h.JSON200 == nil || h.JSON200.Version != "test" || h.JSON200.Images != 5 {
		t.Fatalf("health = %+v, %v", h, err)
	}
	res, err := c.ResolveRefWithResponse(ctx, &client.ResolveRefParams{Ref: host + "/payments/api:1.0"})
	if err != nil || res.JSON200 == nil || res.JSON200.Digest != e.Reg.Digest("api") {
		t.Fatalf("resolve = %+v, %v", res, err)
	}
	if res, _ := c.ResolveRefWithResponse(ctx, &client.ResolveRefParams{Ref: "nope:1"}); res.JSON404 == nil {
		t.Fatalf("resolve missing = %d", res.StatusCode())
	}
	history := true
	img, err := c.GetImageWithResponse(ctx, e.Reg.Digest("api"), &client.GetImageParams{History: &history})
	if err != nil || img.JSON200 == nil || img.JSON200.Image.Repo != host+"/payments/api" || img.JSON200.History == nil {
		t.Fatalf("image = %d %s, %v", img.StatusCode(), img.Body, err)
	}
	if img, _ := c.GetImageWithResponse(ctx, "sha256:"+strings.Repeat("0", 64), nil); img.JSON404 == nil {
		t.Fatalf("missing image = %d", img.StatusCode())
	}
	if img, _ := c.GetImageWithResponse(ctx, "latest", nil); img.StatusCode() != http.StatusBadRequest {
		t.Fatalf("invalid digest = %d", img.StatusCode())
	}
	anc, err := c.GetAncestryWithResponse(ctx, &client.GetAncestryParams{Ref: "payments/api:1.0"})
	if err != nil || anc.JSON200 == nil || len(anc.JSON200.Nodes) != 3 {
		t.Fatalf("ancestry = %d %s", anc.StatusCode(), anc.Body)
	}
	if anc, _ := c.GetAncestryWithResponse(ctx, &client.GetAncestryParams{Ref: "nope:1"}); anc.JSON404 == nil {
		t.Fatalf("ancestry missing = %d", anc.StatusCode())
	}
	depth, all := 0, true
	desc, err := c.GetDescendantsWithResponse(ctx, &client.GetDescendantsParams{Ref: "base/debian:12", Depth: &depth, AllVersions: &all})
	if err != nil || desc.JSON200 == nil || desc.JSON200.Images != 3 {
		t.Fatalf("descendants = %d %s", desc.StatusCode(), desc.Body)
	}
	if d, _ := c.GetDescendantsWithResponse(ctx, &client.GetDescendantsParams{Ref: "nope:1"}); d.JSON404 == nil {
		t.Fatalf("descendants missing = %d", d.StatusCode())
	}
	filter, fields, limit := `label.team = "payments"`, []string{"repo", "tag"}, 10
	q, err := c.RunQueryWithResponse(ctx, &client.RunQueryParams{Filter: &filter, Fields: &fields, Limit: &limit})
	if err != nil || q.JSON200 == nil || len(q.JSON200.Rows) != 2 {
		t.Fatalf("query = %d %s", q.StatusCode(), q.Body)
	}
	bad := "nope = 1"
	if q, _ := c.RunQueryWithResponse(ctx, &client.RunQueryParams{Filter: &bad}); q.JSON400 == nil {
		t.Fatalf("bad query = %d", q.StatusCode())
	}
	g, err := c.GetQueryGraphWithResponse(ctx, &client.GetQueryGraphParams{Filter: &filter})
	if err != nil || g.JSON200 == nil || len(g.JSON200.Nodes) < 3 {
		t.Fatalf("query graph = %d %s", g.StatusCode(), g.Body)
	}
	if g, _ := c.GetQueryGraphWithResponse(ctx, &client.GetQueryGraphParams{Filter: &bad}); g.JSON400 == nil {
		t.Fatalf("bad query graph = %d", g.StatusCode())
	}
	team := "label.team"
	s, err := c.GetStaleWithResponse(ctx, &client.GetStaleParams{GroupBy: &team})
	if err != nil || s.JSON200 == nil || len(s.JSON200.Rows) == 0 {
		t.Fatalf("stale = %d %s", s.StatusCode(), s.Body)
	}
	tag := "tag"
	if s, _ := c.GetStaleWithResponse(ctx, &client.GetStaleParams{GroupBy: &tag}); s.JSON400 == nil {
		t.Fatalf("stale by list field = %d", s.StatusCode())
	}
	roots, err := c.GetRootsWithResponse(ctx)
	if err != nil || roots.JSON200 == nil || len(*roots.JSON200) != 1 {
		t.Fatalf("roots = %d %s", roots.StatusCode(), roots.Body)
	}
	sum, err := c.GetSummaryWithResponse(ctx, &client.GetSummaryParams{GroupBy: &team})
	if err != nil || sum.JSON200 == nil || sum.JSON200.Total.Manifests != 5 {
		t.Fatalf("summary = %d %s", sum.StatusCode(), sum.Body)
	}
	nope := "nope"
	if sum, _ := c.GetSummaryWithResponse(ctx, &client.GetSummaryParams{GroupBy: &nope}); sum.JSON400 == nil {
		t.Fatalf("bad summary = %d", sum.StatusCode())
	}
}

func TestCleanupEndpoints(t *testing.T) {
	t.Parallel()
	c, e := serve(t, Options{})
	where := "tags = 0"
	cl, err := c.GetCleanupWithResponse(ctx, &client.GetCleanupParams{Where: &where})
	if err != nil || cl.JSON200 == nil || cl.JSON200.Where != where {
		t.Fatalf("cleanup = %d %s", cl.StatusCode(), cl.Body)
	}
	all, csv := "", client.Csv
	exp, err := c.GetCleanupWithResponse(ctx, &client.GetCleanupParams{Where: &all, Format: &csv})
	if err != nil || exp.StatusCode() != http.StatusOK || !strings.HasPrefix(string(exp.Body), "repo,digest,images") {
		t.Fatalf("export = %d %s", exp.StatusCode(), exp.Body)
	}
	bad := "nope = 1"
	if r, _ := c.GetCleanupWithResponse(ctx, &client.GetCleanupParams{Where: &bad}); r.JSON400 == nil {
		t.Fatalf("bad cleanup = %d", r.StatusCode())
	}
	days, yes := 1, true
	lc, err := c.GetLifecyclesWithResponse(ctx, &client.GetLifecyclesParams{Days: &days, IncludeConflicts: &yes, Repo: &[]string{"*/payments/*"}})
	if err != nil || lc.JSON200 == nil || len(*lc.JSON200) != 2 || (*lc.JSON200)[0].Policy == nil {
		t.Fatalf("lifecycle = %d %s", lc.StatusCode(), lc.Body)
	}
	if r, _ := c.GetLifecyclesWithResponse(ctx, &client.GetLifecyclesParams{Repo: &[]string{"["}}); r.JSON400 == nil {
		t.Fatalf("bad glob = %d", r.StatusCode())
	}
	_ = e.Svc.Store.Close()
	if r, _ := c.GetCleanupWithResponse(ctx, nil); r.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("cleanup on closed index = %d", r.StatusCode())
	}
	if r, _ := c.GetLifecyclesWithResponse(ctx, nil); r.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("lifecycle on closed index = %d", r.StatusCode())
	}
}

func TestValidationAndAuth(t *testing.T) {
	t.Parallel()
	e := testenv.Indexed(t)
	h, err := New(e.Svc, Options{AuthHeader: "X-Forwarded-User"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	get := func(path string, auth bool) int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if auth {
			req.Header.Set("X-Forwarded-User", "alice")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	if got := get("/v1/healthz", false); got != http.StatusUnauthorized {
		t.Fatalf("no auth header = %d", got)
	}
	if got := get("/v1/healthz", true); got != http.StatusOK {
		t.Fatalf("with auth header = %d", got)
	}
	if got := get("/v1/descendants?ref=x&depth=-1", true); got != http.StatusBadRequest {
		t.Fatalf("depth -1 = %d", got)
	}
	if got := get("/v1/ancestry", true); got != http.StatusBadRequest {
		t.Fatalf("missing ref = %d", got)
	}
	if got := get("/v1/nope", true); got != http.StatusNotFound {
		t.Fatalf("unknown path = %d", got)
	}
}

func TestErrorsFromAClosedIndex(t *testing.T) {
	t.Parallel()
	e := testenv.Indexed(t)
	h, err := New(e.Svc, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = e.Svc.Store.Close()
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, err := client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := c.GetHealthWithResponse(ctx); r.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("health on closed index = %d", r.StatusCode())
	}
	if r, _ := c.GetRootsWithResponse(ctx); r.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("roots on closed index = %d", r.StatusCode())
	}
	if r, _ := c.ResolveRefWithResponse(ctx, &client.ResolveRefParams{Ref: "x"}); r.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("resolve on closed index = %d", r.StatusCode())
	}
	if r, _ := c.GetImageWithResponse(ctx, "sha256:"+strings.Repeat("1", 64), nil); r.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("image on closed index = %d", r.StatusCode())
	}
	if r, _ := c.GetAncestryWithResponse(ctx, &client.GetAncestryParams{Ref: "x"}); r.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("ancestry on closed index = %d", r.StatusCode())
	}
	if r, _ := c.GetDescendantsWithResponse(ctx, &client.GetDescendantsParams{Ref: "x"}); r.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("descendants on closed index = %d", r.StatusCode())
	}
	if r, _ := c.RunQueryWithResponse(ctx, nil); r.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("query on closed index = %d", r.StatusCode())
	}
	if r, _ := c.GetQueryGraphWithResponse(ctx, nil); r.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("query graph on closed index = %d", r.StatusCode())
	}
	if r, _ := c.GetStaleWithResponse(ctx, nil); r.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("stale on closed index = %d", r.StatusCode())
	}
	if r, _ := c.GetSummaryWithResponse(ctx, nil); r.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("summary on closed index = %d", r.StatusCode())
	}
	charts := map[string]func() (int, error){
		"kpis":     func() (int, error) { r, err := c.GetKPIHistoryWithResponse(ctx, nil); return r.StatusCode(), err },
		"storage":  func() (int, error) { r, err := c.GetStorageHistoryWithResponse(ctx, nil); return r.StatusCode(), err },
		"activity": func() (int, error) { r, err := c.GetActivityWithResponse(ctx, nil); return r.StatusCode(), err },
		"ages":     func() (int, error) { r, err := c.GetAgesWithResponse(ctx); return r.StatusCode(), err },
		"coverage": func() (int, error) { r, err := c.GetBaseCoverageWithResponse(ctx); return r.StatusCode(), err },
		"adoption": func() (int, error) { r, err := c.GetAdoptionWithResponse(ctx, nil); return r.StatusCode(), err },
		"rollout": func() (int, error) {
			r, err := c.GetRolloutWithResponse(ctx, &client.GetRolloutParams{Base: "x"})
			return r.StatusCode(), err
		},
	}
	for name, call := range charts {
		if code, err := call(); err != nil || code != http.StatusInternalServerError {
			t.Fatalf("%s on closed index = %d, %v", name, code, err)
		}
	}
}

func TestHelpers(t *testing.T) {
	t.Parallel()
	if str[string](nil) != "" || num(nil, 7) != 7 {
		t.Fatal("helpers")
	}
	if _, err := convert[Table](make(chan int)); err == nil {
		t.Fatal("convert of an unmarshalable value succeeded")
	}
}
