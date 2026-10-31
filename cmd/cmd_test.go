package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/rossedman/strata/internal/model"
	"github.com/rossedman/strata/internal/service"
	"github.com/rossedman/strata/internal/store"
	"github.com/rossedman/strata/internal/testenv"
)

// isolate keeps tests from reading the developer's config or index.
func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	for _, k := range []string{"STRATA_DB", "STRATA_SERVER", "STRATA_SERVE_URL", "STRATA_LOG_LEVEL"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

// reset restores every flag in the tree to its default, since cobra keeps
// flag values between executions of the same command objects.
func reset(c *cobra.Command) {
	for _, fs := range []*pflag.FlagSet{c.Flags(), c.PersistentFlags()} {
		fs.VisitAll(func(f *pflag.Flag) {
			if sv, ok := f.Value.(pflag.SliceValue); ok {
				_ = sv.Replace(nil)
			} else {
				_ = f.Value.Set(f.DefValue)
			}
			f.Changed = false
		})
	}
	// Cobra keeps a subcommand's context from its first run; clear it so
	// each run inherits the context it is executed with.
	c.SetContext(nil) //nolint:staticcheck // nil makes cobra inherit the parent's context
	for _, sub := range c.Commands() {
		reset(sub)
	}
}

// run executes strata with args and returns what it wrote to stdout.
// Logs on stderr are discarded.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	reset(rootCmd)
	cfgFile = ""
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs(args)
	err := rootCmd.ExecuteContext(context.Background())
	return out.String(), err
}

// mustRun runs strata and fails the test on error.
func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := run(t, args...)
	if err != nil {
		t.Fatalf("strata %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// contains fails unless out has every want.
func contains(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestIndexAndReports(t *testing.T) {
	isolate(t)
	e := testenv.IndexedFile(t)
	db := filepath.Join(t.TempDir(), "fresh.duckdb")
	h := e.Reg.Host

	out := mustRun(t, "index", "--db", db, "--registry", h, "--insecure", "--workers", "4", "--external", h+"/base/debian:12")
	contains(t, out, "indexed", "4 repos", "5 images indexed")
	out = mustRun(t, "index", "--db", db, "--registry", h, "--insecure", "-o", "json")
	var res []service.IndexResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res) != 1 || res[0].Images != 5 {
		t.Fatalf("index json = %s, %v", out, err)
	}
	contains(t, mustRun(t, "refresh", "--db", db), "refreshed")

	contains(t, mustRun(t, "explain", "--db", db, h+"/payments/api:1.0", "--history"),
		"Staleness", "Lineage", "team = payments", "Build history", "Findings", "AWS_SECRET_ACCESS_KEY")
	out = mustRun(t, "explain", "--db", db, "payments/api:1.0", "-o", "json")
	var ex service.Explanation
	if err := json.Unmarshal([]byte(out), &ex); err != nil || ex.Image.Repo != h+"/payments/api" {
		t.Fatalf("explain json: %v", err)
	}
	contains(t, mustRun(t, "ancestry", "--db", db, "payments/worker:v1"), "└─", "layers", h+"/base/debian:12")
	for _, f := range []string{"json", "mermaid", "dot", "html", "svg"} {
		if out := mustRun(t, "ancestry", "--db", db, "payments/api:1.0", "-o", f); len(out) < 50 {
			t.Errorf("ancestry -o %s = %q", f, out)
		}
	}
	contains(t, mustRun(t, "descendants", "--db", db, "base/debian:12", "--depth", "0"), "payments/api:1.0", "payments/worker:v1")
	contains(t, mustRun(t, "descendants", "--db", db, "base/debian:12", "--all-versions", "--group-by", "base_version"), "base_version", "exact")
	contains(t, mustRun(t, "descendants", "--db", db, "base/debian:12", "--group-by", "base_version", "-o", "csv"), "base_version,digest")
	contains(t, mustRun(t, "descendants", "--db", db, "base/debian:12", "-o", "json"), `"versions"`)
	contains(t, mustRun(t, "descendants", "--db", db, "base/debian:12", "-o", "mermaid"), "flowchart LR")

	contains(t, mustRun(t, "query", "--db", db, `label.team = "payments"`), "payments/api", "payments/worker")
	contains(t, mustRun(t, "query", "--db", db, `label.team = "payments"`, "--fields", "repo,tag", "--sort", "-created", "--limit", "1", "-o", "csv"), "repo,tag\n")
	contains(t, mustRun(t, "query", "--db", db, "", "-o", "ndjson"), `"digest":`)
	contains(t, mustRun(t, "query", "--db", db, "", "--group-by", "label.team", "-o", "json"), `"images"`)
	contains(t, mustRun(t, "query", "--db", db, `label.team = "payments"`, "--graph"), "└─")
	contains(t, mustRun(t, "query", "--db", db, `label.team = "payments"`, "--graph", "-o", "dot"), "digraph")
	contains(t, mustRun(t, "stale", "--db", db, "--group-by", "label.team"), "label.team", "fresh")
	contains(t, mustRun(t, "roots", "--db", db, "--count-descendants"), "1 distinct bases with 3 descendants")
	contains(t, mustRun(t, "roots", "--db", db, "-o", "json"), `"descendants": 3`)
	contains(t, mustRun(t, "summary", "--db", db), "Registry footprint", "unique layer stacks", "linux/amd64")
	contains(t, mustRun(t, "summary", "--db", db, "--group-by", "repo"), "exclusive_bytes")
	contains(t, mustRun(t, "summary", "--db", db, "-o", "json"), `"dedup_bytes"`)
	contains(t, mustRun(t, "summary", "--db", db, "--group-by", "repo", "-o", "csv"), "group,repos")
	contains(t, mustRun(t, "labels", "--db", db), "team", "coverage_pct")
	contains(t, mustRun(t, "labels", "--db", db, "-o", "json"), `"key": "team"`)
	contains(t, mustRun(t, "labels", "--db", db, "team"), "payments", "repos lacking it")
	contains(t, mustRun(t, "labels", "--db", db, "team", "-o", "json"), `"values"`)
	contains(t, mustRun(t, "labels", "--db", db, "--similar"), "no results")
	contains(t, mustRun(t, "labels", "--db", db, "--similar", "-o", "json"), "[]")
	contains(t, mustRun(t, "layers", "top", "--db", db, "--by", "size"), "blob_digest")
	contains(t, mustRun(t, "layers", "top", "--db", db, "-o", "json"), `"images"`)
	contains(t, mustRun(t, "layers", "bloat", "--db", db, "--min-size", "1", "--group-by", "repo"), "blob_digest")
	contains(t, mustRun(t, "bases", "similar", "--db", db), "no results")
	contains(t, mustRun(t, "bases", "similar", "--db", db, "-o", "json"), "[]")
	contains(t, mustRun(t, "diff", "--db", db, "--since", "7d", "--dark-days", "0"), "Changes from", "Dark repos")
	contains(t, mustRun(t, "diff", "--db", db, "-o", "json"), `"changes"`)
	contains(t, mustRun(t, "trend", "--db", db, "manifests"), "<polyline")
	contains(t, mustRun(t, "trend", "--db", db, "dedup_bytes", "--group-by", "repo", "-o", "csv"), "day,group,dedup_bytes")
	contains(t, mustRun(t, "map", "--db", db), "registry map")
	contains(t, mustRun(t, "map", "--db", db, "-o", "json"), `"kind": "map"`)

	// Scans with findings on debian reach everything built on it.
	st, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	werr := st.Write(context.Background(), &store.Batch{Scans: []model.Scan{{Digest: e.Reg.Digest("debian"), Status: model.ScanComplete, Critical: 2,
		HasFindings: true, FetchedAt: time.Now(), Findings: []model.Finding{{ID: "CVE-2024-3094", Package: "xz-utils", Severity: "critical", FixedIn: "5.6.1"}}}}})
	if err := errors.Join(werr, st.Close()); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "refresh", "--db", db)
	contains(t, mustRun(t, "vulns", "bases", "--db", db), "base/debian:12", "critical", "in_use")
	contains(t, mustRun(t, "vulns", "bases", "--db", db, "-o", "json"), `"fixable": 1`)
	contains(t, mustRun(t, "vulns", "impact", "--db", db, "base/debian:12"), "reaches 3 images", "payments", "enters through")
	contains(t, mustRun(t, "vulns", "impact", "--db", db, "CVE-2024-3094", "--images"), "inherited", "scan")
	contains(t, mustRun(t, "vulns", "impact", "--db", db, "CVE-2024-3094", "-o", "csv"), "digest,ref,repo,group,critical")
	contains(t, mustRun(t, "vulns", "impact", "--db", db, "CVE-2024-3094", "-o", "json"), `"origins"`)
	contains(t, mustRun(t, "vulns", "impact", "--db", db, "CVE-9999-1"), "reaches 0 images")
	contains(t, mustRun(t, "query", "--db", db, `vuln.id = "CVE-2024-3094"`, "--fields", "repo,vuln.critical"), "base/debian")

	contains(t, mustRun(t, "cleanup", "--db", db), "cleanup", "candidates", "reclaimable")
	contains(t, mustRun(t, "cleanup", "--db", db, "--where", "descendants = 0", "--limit", "1"), "largest 1 of 2 listed", "exclusive_bytes", "payments")
	contains(t, mustRun(t, "cleanup", "--db", db, "--where", `repo ~ "*/base/*"`), "held: kept images are built on them")
	contains(t, mustRun(t, "cleanup", "--db", db, "--where", "descendants = 0", "--scope", "repo", "-o", "csv"), "repo,digest,images,size,exclusive_bytes")
	contains(t, mustRun(t, "cleanup", "--db", db, "-o", "json"), `"reclaimable_bytes"`)
	contains(t, mustRun(t, "cleanup", "lifecycle", "--db", db), "would_expire", "policy")
	contains(t, mustRun(t, "cleanup", "lifecycle", "--db", db, "--days", "1", "-o", "json"), `"conflicts"`)
	contains(t, mustRun(t, "cleanup", "lifecycle", "--db", db, "--days", "1", "--include-conflicts", "-o", "script"), "aws ecr put-lifecycle-policy --repository-name payments/api", `"countNumber":1`)

	parquet := filepath.Join(t.TempDir(), "history.parquet")
	contains(t, mustRun(t, "history", "export", "--db", db, parquet), "exported")
	contains(t, mustRun(t, "history", "import", "--db", filepath.Join(t.TempDir(), "other.duckdb"), parquet), "imported")

	contains(t, mustRun(t, "version"), "strata dev", "index format 1")
	contains(t, mustRun(t, "version", "-o", "json"), `"index_format": 1`)
	contains(t, mustRun(t, "--help"), "strata index --registry reg.io")
	contains(t, mustRun(t, "query", "--help"), "has:sbom", "ancestor")
}

func TestLifecycleScript(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	pol := &service.LifecyclePolicy{Rules: []service.LifecycleRule{{RulePriority: 1, Action: service.LifecycleAction{Type: "expire"}}}}
	err := lifecycleScript(&b, []service.Lifecycle{
		{Repo: "123456789012.dkr.ecr.eu-west-1.amazonaws.com/team/app", Name: "team/app", Expires: 3, Policy: pol},
		{Repo: "reg.io/base/os", Name: "base/os", Skipped: "would expire 1 image(s) strata keeps"},
	})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, b.String(), "set -eu",
		"aws ecr put-lifecycle-policy --registry-id 123456789012 --region eu-west-1 --repository-name team/app --lifecycle-policy-text '{\"rules\"",
		"# reg.io/base/os: skipped, would expire 1 image(s) strata keeps")
}

func TestCommandErrors(t *testing.T) {
	isolate(t)
	e := testenv.IndexedFile(t)
	db := e.DB
	cases := [][]string{
		{"index", "--db", db},
		{"explain", "--db", db, "nope:1"},
		{"explain", "--db", db, "x", "-o", "yaml"},
		{"ancestry", "--db", db, "x", "-o", "png"},
		{"ancestry", "--db", db, "nope:1"},
		{"descendants", "--db", db, "x", "--group-by", "repo"},
		{"descendants", "--db", db, "x", "--group-by", "base_version", "-o", "svg"},
		{"descendants", "--db", db, "x", "-o", "csv"},
		{"descendants", "--db", db, "nope:1"},
		{"query", "--db", db, "x", "-o", "svg"},
		{"query", "--db", db, "x", "--graph", "-o", "csv"},
		{"query", "--db", db, "nope = 1"},
		{"query", "--db", db, "nope = 1", "--graph"},
		{"query", "--db", db, "", "--fields", "nope"},
		{"stale", "--db", db, "-o", "xml"},
		{"stale", "--db", db, "--group-by", "tag"},
		{"roots", "--db", db, "-o", "xml"},
		{"summary", "--db", db, "-o", "ndjson"},
		{"summary", "--db", db, "--group-by", "nope"},
		{"labels", "--db", db, "-o", "xml"},
		{"layers", "top", "--db", db, "--by", "color"},
		{"layers", "top", "--db", db, "-o", "xml"},
		{"layers", "bloat", "--db", db, "--group-by", "team"},
		{"layers", "bloat", "--db", db, "--min-size", "huge"},
		{"layers", "bloat", "--db", db, "-o", "xml"},
		{"bases", "similar", "--db", db, "-o", "xml"},
		{"diff", "--db", db, "--since", "soon"},
		{"diff", "--db", db, "-o", "csv"},
		{"diff", "--db", filepath.Join(t.TempDir(), "empty.duckdb")},
		{"trend", "--db", db, "nope"},
		{"vulns", "bases", "--db", db, "-o", "xml"},
		{"vulns", "bases", "--db", db, "--in-use", "soon"},
		{"vulns", "bases", "--db", db, "--severity", "spicy"},
		{"vulns", "impact", "--db", db, "x", "-o", "xml"},
		{"vulns", "impact", "--db", db, "x", "--in-use", "soon"},
		{"vulns", "impact", "--db", db, "nope:1"},
		{"vulns", "impact", "--db", db, "CVE-1", "--where", "nope = 1"},
		{"cleanup", "--db", db, "-o", "xml"},
		{"cleanup", "--db", db, "--scope", "galaxy"},
		{"cleanup", "lifecycle", "--db", db, "-o", "csv"},
		{"cleanup", "lifecycle", "--db", db, "--repo", "["},
		{"trend", "--db", db, "manifests", "-o", "svg"},
		{"map", "--db", db, "-o", "svg"},
		{"history", "import", "--db", db, "/nope.parquet"},
		{"history", "export", "--db", db, "/nope/dir/x.parquet"},
		{"serve", "--db", db, "--addr", "0.0.0.0:0"},
		{"serve", "--db", db, "--addr", "nope"},
		{"ui", "--db", db, "--addr", "0.0.0.0:0"},
		{"ui", "--db", db, "--addr", "nope"},
		{"version", "--log-level", "loud"},
		{"version", "--config", "/nope/strata.yaml"},
		{"refresh", "--db", "/dev/null/x.duckdb"},
	}
	for _, args := range cases {
		if out, err := run(t, args...); err == nil {
			t.Errorf("strata %s succeeded:\n%s", strings.Join(args, " "), out)
		}
	}
}

func TestConfigFile(t *testing.T) {
	isolate(t)
	e := testenv.IndexedFile(t)
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(policy, []byte("eol_roots: [\"*/base/debian:*\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "strata.yaml")
	body := "db: " + e.DB + "\npolicy: " + policy + "\nrequired: [team]\nregistries:\n  - host: " + e.Reg.Host + "\n    insecure: true\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	contains(t, mustRun(t, "index", "--config", cfg), "indexed")
	// debian is now an EOL root, so everything built on it is stale.
	contains(t, mustRun(t, "query", "--config", cfg, `stale = "stale"`, "--fields", "repo"), "payments/api")
	contains(t, mustRun(t, "summary", "--config", cfg), "required labels")
	// The environment overrides the file, and a flag overrides both.
	t.Setenv("STRATA_DB", filepath.Join(dir, "env.duckdb"))
	contains(t, mustRun(t, "summary", "--config", cfg), "manifests            0")
	contains(t, mustRun(t, "summary", "--config", cfg, "--db", e.DB), "manifests            5")

	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("registries:\n  - insecure: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "version", "--config", bad); err == nil {
		t.Fatal("invalid config accepted")
	}
	if err := os.WriteFile(bad, []byte("policy: "+filepath.Join(dir, "missing.yaml")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "stale", "--config", bad, "--db", e.DB); err == nil {
		t.Fatal("missing policy file accepted")
	}
}

func TestServeAndRemote(t *testing.T) {
	isolate(t)
	e := testenv.Indexed(t)
	if err := checkShared("127.0.0.1:0", ""); err != nil {
		t.Fatal(err)
	}
	if err := checkShared("localhost:80", ""); err != nil {
		t.Fatal(err)
	}
	if err := checkShared(":8080", "X-User"); err != nil {
		t.Fatal(err)
	}
	// Commands use state for the logger and config.
	if _, err := run(t, "version"); err != nil {
		t.Fatal(err)
	}
	h, err := serveHandler(e.Svc, "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	host := e.Reg.Host
	contains(t, mustRun(t, "explain", "--server", srv.URL, host+"/payments/api:1.0"), "team = payments")
	contains(t, mustRun(t, "ancestry", "--server", srv.URL, "payments/api:1.0"), "└─")
	contains(t, mustRun(t, "descendants", "--server", srv.URL, "base/debian:12", "--depth", "0", "--all-versions"), "payments/api:1.0")
	contains(t, mustRun(t, "query", "--server", srv.URL, `label.team = "payments"`, "--fields", "repo", "--sort", "repo"), "payments/worker")
	contains(t, mustRun(t, "query", "--server", srv.URL, "", "--graph", "-o", "json"), `"nodes"`)
	contains(t, mustRun(t, "stale", "--server", srv.URL), "scope")
	contains(t, mustRun(t, "roots", "--server", srv.URL), "base/debian:12")
	contains(t, mustRun(t, "summary", "--server", srv.URL, "--group-by", "repo"), "Registry footprint")
	if out, err := run(t, "explain", "--server", srv.URL, "nope:1"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("remote not found = %v\n%s", err, out)
	}
	if _, err := run(t, "query", "--server", srv.URL, "nope = 1"); err == nil {
		t.Fatal("remote bad filter succeeded")
	}
	if _, err := run(t, "descendants", "--server", srv.URL, "x", "--versions-from", "f"); err == nil {
		t.Fatal("remote versions-from succeeded")
	}
	if _, err := run(t, "stale", "--server", "http://127.0.0.1:1"); err == nil {
		t.Fatal("unreachable server succeeded")
	}
	// /openapi.json, /mcp and the web UI are mounted next to the API.
	contains(t, get(t, srv.URL+"/openapi.json"), `"openapi"`)
	contains(t, get(t, srv.URL+"/"), `<script src="app.js">`)

	authed, err := serveHandler(e.Svc, "X-User")
	if err != nil {
		t.Fatal(err)
	}
	asrv := httptest.NewServer(authed)
	defer asrv.Close()
	if code := status(t, asrv.URL+"/mcp"); code != 401 {
		t.Fatalf("mcp without auth = %d", code)
	}
	t.Setenv("STRATA_SERVE_AUTH_HEADER", "X-User")
	contains(t, mustRun(t, "stale", "--server", asrv.URL), "scope")
}

func TestServeRuns(t *testing.T) {
	isolate(t)
	e := testenv.IndexedFile(t)
	reset(rootCmd)
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"serve", "--db", e.DB, "--addr", "127.0.0.1:0", "--reindex-every", "50ms", "--log-level", "debug"})
	t.Setenv("STRATA_REGISTRIES", "")
	cfg := filepath.Join(t.TempDir(), "strata.yaml")
	body := "registries:\n  - host: " + e.Reg.Host + "\n    insecure: true\n  - host: 127.0.0.1:1\n    insecure: true\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgFile = cfg
	done := make(chan error, 1)
	go func() { done <- rootCmd.ExecuteContext(ctx) }()
	time.Sleep(1500 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v\n%s", err, out.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
	contains(t, out.String(), "serving", "INFO  indexed", "index failed")
}

func TestMCPCommand(t *testing.T) {
	isolate(t)
	e := testenv.IndexedFile(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	_ = w.Close() // EOF: the server stops at once
	if _, err := run(t, "mcp", "--db", e.DB); err != nil && !strings.Contains(err.Error(), "EOF") && !strings.Contains(err.Error(), "closed") {
		t.Fatalf("mcp: %v", err)
	}
}

// httpClient is used for raw requests in tests.
var httpClient = &http.Client{Timeout: 10 * time.Second}

// get fetches a URL's body.
func get(t *testing.T, url string) string {
	t.Helper()
	res, err := httpClient.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var b bytes.Buffer
	_, _ = b.ReadFrom(res.Body)
	return b.String()
}

// status fetches a URL and returns the status code.
func status(t *testing.T, url string) int {
	t.Helper()
	res, err := httpClient.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return res.StatusCode
}

func TestUIRuns(t *testing.T) {
	isolate(t)
	e := testenv.IndexedFile(t)
	reset(rootCmd)
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"ui", "--db", e.DB, "--addr", "127.0.0.1:0"})
	done := make(chan error, 1)
	go func() { done <- rootCmd.ExecuteContext(ctx) }()
	time.Sleep(500 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ui: %v\n%s", err, out.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ui did not stop")
	}
	contains(t, out.String(), "serving", "ui=http://127.0.0.1:")
}
