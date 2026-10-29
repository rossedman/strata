package crawl

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/rossedman/strata/internal/fixture"
	"github.com/rossedman/strata/internal/model"
	"github.com/rossedman/strata/internal/store"
	"github.com/rossedman/strata/internal/testreg"
)

// opts returns crawl options for a test registry.
func opts(r *testreg.Registry) Options {
	return Options{
		Registry:     r.Host,
		Insecure:     true,
		Referrers:    true,
		Workers:      4,
		EnvAllowlist: []string{"GO_VERSION"},
		Keychain:     authn.NewMultiKeychain(),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:          func() time.Time { return fixture.Epoch },
	}
}

// count runs a count query.
func count(t *testing.T, s *store.Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestCrawl(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := testreg.Start(t)
	s := fixture.Open(t, nil)
	stats, err := Crawl(ctx, s, opts(r))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Repos != 4 || stats.Images != 5 || stats.Requests == 0 {
		t.Fatalf("stats = %+v", stats)
	}
	// debian, go amd64 and arm64, api, worker; the signature and SBOM are
	// artifacts, not images.
	if n := count(t, s, `SELECT count(*) FROM images`); n != 5 {
		t.Fatalf("images = %d", n)
	}
	if n := count(t, s, `SELECT count(*) FROM index_manifests`); n != 2 {
		t.Fatalf("index entries = %d", n)
	}
	if n := count(t, s, `SELECT count(*) FROM tags`); n != 6 {
		t.Fatalf("tags = %d", n)
	}
	api := r.Digest("api")
	var user, envKeys, history, team, ports string
	var healthy bool
	err = s.DB().QueryRow(`
		SELECT i.config_user, array_to_string(i.config_env_keys, ','), array_to_string(i.config_ports, ','),
			i.config_healthcheck, l.labels['team']::VARCHAR,
			(SELECT string_agg(created_by, '|') FROM image_history h WHERE h.digest = i.digest)
		FROM images i JOIN labels l USING (digest) WHERE i.digest = ?`, api).Scan(&user, &envKeys, &ports, &healthy, &team, &history)
	if err != nil {
		t.Fatal(err)
	}
	if user != "root" || envKeys != "PATH,GO_VERSION,AWS_SECRET_ACCESS_KEY" || team != "payments" || ports != "22/tcp,8080/tcp" || !healthy {
		t.Fatalf("api fields: user=%s env=%s team=%s ports=%s health=%v", user, envKeys, team, ports, healthy)
	}
	if strings.Contains(history, "abc123") || strings.Contains(history, ":pw@") || !strings.Contains(history, "TOKEN=***") {
		t.Fatalf("history not redacted: %s", history)
	}
	if n := count(t, s, `SELECT count(*) FROM env_values WHERE key = 'GO_VERSION' AND value = '1.22'`); n != 1 {
		t.Fatal("allowlisted env value not stored")
	}
	if n := count(t, s, `SELECT count(*) FROM env_values WHERE key = 'AWS_SECRET_ACCESS_KEY'`); n != 0 {
		t.Fatal("secret env value stored")
	}
	kinds := map[string]int{}
	rows, err := s.DB().Query(`SELECT kind, count(*) FROM referrers WHERE subject = ? GROUP BY kind`, api)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			t.Fatal(err)
		}
		kinds[k] = n
	}
	_ = rows.Close()
	if kinds[model.KindSignature] != 1 || kinds[model.KindSBOM] != 1 {
		t.Fatalf("referrer kinds = %v", kinds)
	}
	var arch, variant, index string
	if err := s.DB().QueryRow(`SELECT arch, variant, index_digest FROM images WHERE digest = ?`, r.Digest("go-arm64")).Scan(&arch, &variant, &index); err != nil {
		t.Fatal(err)
	}
	if idx, _ := r.GoIndex.Digest(); arch != "arm64" || variant != "v8" || index != idx.String() {
		t.Fatalf("arm64 = %s %s %s", arch, variant, index)
	}
}

func TestRecrawlRecordsMoves(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := testreg.Start(t)
	s := fixture.Open(t, nil)
	o := opts(r)
	if _, err := Crawl(ctx, s, o); err != nil {
		t.Fatal(err)
	}
	// Nothing changed: no new images, no moves, and only cheap requests.
	stats, err := Crawl(ctx, s, o)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Images != 0 || stats.TagMoves != 0 {
		t.Fatalf("idle re-crawl = %+v", stats)
	}
	// Move latest to a new build and delete 1.0.
	api2 := r.Layered("go-amd64", "api2", time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC), nil, "RUN build api v2")
	r.Push("payments/api:latest", api2)
	r.Delete("payments/api:1.0")
	o.Now = func() time.Time { return fixture.Epoch.AddDate(0, 0, 3) }
	stats, err = Crawl(ctx, s, o)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Images != 1 || stats.TagMoves != 2 {
		t.Fatalf("re-crawl after push = %+v", stats)
	}
	repo := r.Host + "/payments/api"
	if n := count(t, s, `SELECT count(*) FROM tag_history WHERE repo = ? AND tag = 'latest'`, repo); n != 2 {
		t.Fatalf("latest history rows = %d", n)
	}
	if n := count(t, s, `SELECT count(*) FROM tag_history WHERE repo = ? AND tag = '1.0' AND digest IS NULL`, repo); n != 1 {
		t.Fatal("tag deletion not recorded")
	}
	if n := count(t, s, `SELECT count(*) FROM tags WHERE repo = ? AND tag = '1.0'`, repo); n != 0 {
		t.Fatal("deleted tag still present")
	}
}

func TestResume(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := testreg.Start(t)
	s := fixture.Open(t, nil)
	run, _, err := s.StartCrawl(ctx, r.Host)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckpointRepo(ctx, run, r.Host+"/base/debian"); err != nil {
		t.Fatal(err)
	}
	stats, err := Crawl(ctx, s, opts(r))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Skipped != 1 || stats.Repos != 3 {
		t.Fatalf("resume stats = %+v", stats)
	}
	// The run finished, so the next crawl starts fresh.
	if stats, err = Crawl(ctx, s, opts(r)); err != nil || stats.Skipped != 0 {
		t.Fatalf("next crawl = %+v, %v", stats, err)
	}
}

func TestSeededReposAndExternal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := testreg.Start(t)
	s := fixture.Open(t, nil)
	o := opts(r)
	o.Repos = []string{"payments/worker"}
	o.External = []string{r.Host + "/base/debian:12", r.Host + "/base/go@" + mustDigest(t, r.GoIndex)}
	o.RequestsPerSecond = 1000
	o.Referrers = false
	stats, err := Crawl(ctx, s, o)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Repos != 1 || stats.Images != 4 {
		t.Fatalf("stats = %+v", stats)
	}
	if n := count(t, s, `SELECT count(*) FROM images WHERE external`); n != 3 {
		t.Fatalf("external images = %d", n)
	}
	// Fetching the same external base again does nothing.
	o.Registry = ""
	if stats, err = Crawl(ctx, s, o); err != nil || stats.Images != 0 {
		t.Fatalf("second external crawl = %+v, %v", stats, err)
	}
}

// mustDigest returns an index's digest.
func mustDigest(t *testing.T, idx v1.ImageIndex) string {
	t.Helper()
	d, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d.String()
}

func TestCrawlErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := fixture.Open(t, nil)
	r := testreg.Start(t)
	tests := []struct {
		name string
		mod  func(*Options)
		msg  string
	}{
		{"bad redaction", func(o *Options) { o.Redactions = []string{"("} }, "redaction pattern"},
		{"bad registry", func(o *Options) { o.Registry = "bad host!" }, "registry"},
		{"missing repo", func(o *Options) { o.Repos = []string{"nope/nope"} }, "nope/nope"},
		{"bad repo name", func(o *Options) { o.Repos = []string{"UPPER"} }, "repo"},
		{"bad external", func(o *Options) { o.Registry = ""; o.External = []string{"::"} }, "external base"},
		{"missing external", func(o *Options) { o.Registry = ""; o.External = []string{r.Host + "/nope:1"} }, "resolve external"},
		{"unreachable", func(o *Options) {
			o.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF })
		}, "list repos"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := opts(r)
			tt.mod(&o)
			_, err := Crawl(ctx, s, o)
			if err == nil || !strings.Contains(err.Error(), tt.msg) {
				t.Fatalf("error = %v, want %q", err, tt.msg)
			}
		})
	}
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip calls the function.
func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRedactor(t *testing.T) {
	t.Parallel()
	red, err := NewRedactor(DefaultRedactions)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		`RUN GITHUB_TOKEN=ghp_abc make`:             `RUN GITHUB_TOKEN=*** make`,
		`ARG password="s3cr3t pass"`:                `ARG password=***`,
		`RUN curl https://bob:pw@x.io/a`:            `RUN curl https://bob:***@x.io/a`,
		`RUN curl -H "Authorization: Bearer xyz" u`: `RUN curl -H "Authorization: Bearer ***" u`,
		`RUN apt-get install -y curl`:               `RUN apt-get install -y curl`,
		`ENV API_KEY: abc`:                          `ENV API_KEY: ***`,
	}
	for in, want := range tests {
		if got := red.Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
	whole, err := NewRedactor([]string{`secret`})
	if err != nil {
		t.Fatal(err)
	}
	if got := whole.Redact("a secret b"); got != "a *** b" {
		t.Fatalf("whole-match redaction = %q", got)
	}
}

func TestHelpers(t *testing.T) {
	t.Parallel()
	if !isImageConfig("") || isImageConfig("application/vnd.cncf.helm.config.v1+json") {
		t.Fatal("isImageConfig")
	}
	if _, ok := cosignReferrer("v1", "x"); ok {
		t.Fatal("ordinary tag treated as cosign")
	}
	r, ok := cosignReferrer("sha256-"+strings.Repeat("a", 64)+".att", "d")
	if !ok || r.Kind != model.KindAttestation {
		t.Fatalf("cosign attestation = %+v", r)
	}
	if !isAttestationManifest(v1.Descriptor{Platform: &v1.Platform{OS: "unknown"}}) {
		t.Fatal("unknown platform is an attestation manifest")
	}
}

func BenchmarkRedact(b *testing.B) {
	red, err := NewRedactor(DefaultRedactions)
	if err != nil {
		b.Fatal(err)
	}
	cmd := `/bin/sh -c apt-get update && apt-get install -y curl ca-certificates && curl -fsSL https://user:pw@example.com/install.sh | TOKEN=abc sh`
	b.ReportAllocs()
	for b.Loop() {
		red.Redact(cmd)
	}
}

func FuzzRedact(f *testing.F) {
	f.Add("RUN TOKEN=abc ./x")
	f.Add("https://u:p@h/")
	f.Add("Authorization: Basic Zm9v")
	red, err := NewRedactor(DefaultRedactions)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := red.Redact(s)
		// Redaction is idempotent: redacting twice changes nothing more.
		if again := red.Redact(out); again != out {
			t.Fatalf("not idempotent: %q -> %q -> %q", s, out, again)
		}
	})
}
