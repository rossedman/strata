package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/api"
	"github.com/rossedman/strata/internal/service"
	"github.com/rossedman/strata/internal/store"
	"github.com/rossedman/strata/internal/testenv"
)

var ctx = context.Background()

// command returns a cobra command with the flags strata's root defines.
func command() *cobra.Command {
	c := &cobra.Command{Use: "x"}
	c.Flags().String("db", "", "")
	c.Flags().String("log-level", "", "")
	c.Flags().String("server", "", "")
	c.Flags().String("addr", "", "")
	c.SetErr(&bytes.Buffer{})
	return c
}

// isolate points config lookup at an empty home.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	t.Chdir(dir)
	return dir
}

func TestLoad(t *testing.T) {
	dir := isolate(t)
	c := command()
	s, err := Load(c, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Config.Platform != "linux/amd64" || !s.Config.Referrers || s.Config.Serve.Addr != "127.0.0.1:7878" {
		t.Fatalf("defaults = %+v", s.Config)
	}
	// The XDG config is found first; flags beat the environment, which
	// beats the file.
	cfg := filepath.Join(dir, "xdg", "strata", "strata.yaml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("db: from-file\nplatform: linux/arm64\nserve:\n  addr: 127.0.0.1:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := findConfig(); got != cfg {
		t.Fatalf("findConfig = %s", got)
	}
	s, err = Load(command(), "")
	if err != nil || s.Config.DB != "from-file" || s.Config.Platform != "linux/arm64" {
		t.Fatalf("file config = %+v, %v", s.Config, err)
	}
	t.Setenv("STRATA_DB", "from-env")
	if s, _ = Load(command(), ""); s.Config.DB != "from-env" {
		t.Fatalf("env db = %s", s.Config.DB)
	}
	c = command()
	if err := c.Flags().Set("db", "from-flag"); err != nil {
		t.Fatal(err)
	}
	if err := c.Flags().Set("addr", "127.0.0.1:2"); err != nil {
		t.Fatal(err)
	}
	if s, _ = Load(c, ""); s.Config.DB != "from-flag" || s.Config.Serve.Addr != "127.0.0.1:2" {
		t.Fatalf("flag db = %s addr = %s", s.Config.DB, s.Config.Serve.Addr)
	}
	if len(ConfigPaths()) != 4 {
		t.Fatalf("ConfigPaths = %v", ConfigPaths())
	}
	if _, err := Load(command(), filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("missing --config accepted")
	}
	if err := os.WriteFile(cfg, []byte("registries: {"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(command(), ""); err == nil {
		t.Fatal("broken config accepted")
	}
	if err := os.WriteFile(cfg, []byte("registries: 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(command(), ""); err == nil {
		t.Fatal("mistyped config accepted")
	}
	if err := os.WriteFile(cfg, []byte("log-level: loud\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(command(), ""); err == nil {
		t.Fatal("bad log level accepted")
	}
}

func TestOpenAndHelpers(t *testing.T) {
	isolate(t)
	db := filepath.Join(t.TempDir(), "x.duckdb")
	c := command()
	_ = c.Flags().Set("db", db)
	s, err := Load(c, "")
	if err != nil {
		t.Fatal(err)
	}
	s.Config.LineageLabels = []string{"base"}
	s.Config.Platform = "linux/arm64"
	svc, err := s.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if svc.Platform != "linux/arm64" || len(svc.Lineage.Labels) != 1 {
		t.Fatalf("service = %+v", svc)
	}
	var cerr error
	Close(svc, &cerr)
	if cerr != nil {
		t.Fatal(cerr)
	}
	s.Config.PolicyFile = "/nope.yaml"
	if _, err := s.Open(ctx); err == nil {
		t.Fatal("bad policy accepted")
	}
	s.Config.DB = "/dev/null/x.duckdb"
	if _, err := s.Open(ctx); err == nil {
		t.Fatal("bad db accepted")
	}
	if Out(c) == nil {
		t.Fatal("Out")
	}
	if OneOf("o", "a", "a", "b") != nil || OneOf("o", "c", "a", "b") == nil {
		t.Fatal("OneOf")
	}
	called := false
	if err := Spin(ctx, c, "x", func(context.Context) error { called = true; return nil }); err != nil || !called {
		t.Fatal("Spin without a terminal should just run the action")
	}
	e2 := errors.New("first")
	Release(func() error { return errors.New("second") }, &e2)
	if e2.Error() != "first" {
		t.Fatal("Release overwrote the first error")
	}
	var e3 error
	Release(func() error { return errors.New("second") }, &e3)
	if e3 == nil {
		t.Fatal("Release dropped the close error")
	}
	if isLocked(nil) || !isLocked(errors.New("IO Error: Could not set lock on file")) {
		t.Fatal("isLocked")
	}
}

// serveAPI serves the API over an indexed registry.
func serveAPI(t *testing.T, header string) (*httptest.Server, *testenv.Env) {
	t.Helper()
	e := testenv.Indexed(t)
	h, err := api.New(e.Svc, api.Options{AuthHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, e
}

func TestRemote(t *testing.T) {
	t.Parallel()
	srv, e := serveAPI(t, "X-User")
	r, err := NewRemote(srv.URL+"/", "X-User")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Healthy(ctx) {
		t.Fatal("server unhealthy")
	}
	ex, err := r.Explain(ctx, "payments/api:1.0", "", true)
	if err != nil || ex.Image.Repo != e.Reg.Host+"/payments/api" || len(ex.History) == 0 {
		t.Fatalf("explain = %+v, %v", ex, err)
	}
	if _, err := r.Explain(ctx, "nope:1", "", false); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("explain missing = %v", err)
	}
	if g, err := r.Ancestry(ctx, "payments/api:1.0", ""); err != nil || len(g.Nodes) != 3 {
		t.Fatalf("ancestry = %v", err)
	}
	if d, err := r.Descendants(ctx, "base/debian:12", service.DescendantOptions{Depth: 0, AllVersions: true}); err != nil || d.Images != 3 {
		t.Fatalf("descendants = %+v, %v", d, err)
	}
	if q, err := r.Query(ctx, service.QueryOptions{Filter: `label.team = "payments"`, Fields: []string{"repo"}, Sort: []string{"repo"}}); err != nil || len(q.Rows) != 2 {
		t.Fatalf("query = %+v, %v", q, err)
	}
	if _, err := r.Query(ctx, service.QueryOptions{Filter: "nope = 1"}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("bad query = %v", err)
	}
	if g, err := r.QueryGraph(ctx, "", 10); err != nil || len(g.Nodes) == 0 {
		t.Fatalf("query graph = %v", err)
	}
	if st, err := r.Stale(ctx, "repo"); err != nil || len(st.Rows) == 0 {
		t.Fatalf("stale = %v", err)
	}
	if roots, err := r.Roots(ctx); err != nil || len(roots) != 1 {
		t.Fatalf("roots = %v", err)
	}
	if sum, err := r.Summary(ctx, ""); err != nil || sum.Total.Manifests != 5 {
		t.Fatalf("summary = %v", err)
	}
	if c, err := r.Cleanup(ctx, service.CleanupOptions{Where: "tags = 0", Scope: service.ScopeRepo, Limit: 5}); err != nil || c.Scope != service.ScopeRepo {
		t.Fatalf("cleanup = %+v, %v", c, err)
	}
	if lcs, err := r.Lifecycles(ctx, service.LifecycleOptions{Repos: []string{"*/base/*"}, Days: 1, IncludeConflicts: true}); err != nil || len(lcs) != 2 {
		t.Fatalf("lifecycles = %+v, %v", lcs, err)
	}

	// Without the header every call fails cleanly.
	bare, _ := NewRemote(srv.URL, "")
	if bare.Healthy(ctx) {
		t.Fatal("unauthenticated health succeeded")
	}
	if _, err := bare.Stale(ctx, ""); err == nil {
		t.Fatal("unauthenticated call succeeded")
	}
	down, _ := NewRemote("http://127.0.0.1:1", "")
	for _, f := range []func() error{
		func() error { _, err := down.Explain(ctx, "x", "", false); return err },
		func() error { _, err := down.Ancestry(ctx, "x", ""); return err },
		func() error { _, err := down.Descendants(ctx, "x", service.DescendantOptions{}); return err },
		func() error { _, err := down.Query(ctx, service.QueryOptions{}); return err },
		func() error { _, err := down.QueryGraph(ctx, "", 1); return err },
		func() error { _, err := down.Stale(ctx, ""); return err },
		func() error { _, err := down.Roots(ctx); return err },
		func() error { _, err := down.Summary(ctx, ""); return err },
		func() error { _, err := down.Cleanup(ctx, service.CleanupOptions{}); return err },
		func() error { _, err := down.Lifecycles(ctx, service.LifecycleOptions{}); return err },
	} {
		if f() == nil {
			t.Error("call to an unreachable server succeeded")
		}
	}
	if _, err := decode[int](http.StatusTeapot, []byte("not json"), nil); err == nil {
		t.Fatal("decode of a bad status succeeded")
	}
}

// TestHoldLock is not a test: run as a subprocess, it opens the index
// named by STRATA_HOLD_DB and holds it until killed.
func TestHoldLock(t *testing.T) {
	db := os.Getenv("STRATA_HOLD_DB")
	if db == "" {
		t.Skip("helper process only")
	}
	st, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_, _ = os.Stdout.WriteString("locked\n")
	time.Sleep(time.Minute)
}

func TestBackendRoutesAroundALock(t *testing.T) {
	isolate(t)
	srv, _ := serveAPI(t, "")
	// Another process holds a second index file.
	e := testenv.IndexedFile(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestHoldLock$") // #nosec G204 -- re-runs this test binary.
	cmd.Env = append(os.Environ(), "STRATA_HOLD_DB="+e.DB)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	buf := make([]byte, 7)
	if _, err := out.Read(buf); err != nil || string(buf) != "locked\n" {
		t.Fatalf("helper did not lock: %q %v", buf, err)
	}
	c := command()
	_ = c.Flags().Set("db", e.DB)
	_ = c.Flags().Set("addr", strings.TrimPrefix(srv.URL, "http://"))
	s, err := Load(c, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx); err == nil || !strings.Contains(err.Error(), "strata serve") {
		t.Fatalf("open of a locked index = %v", err)
	}
	b, release, err := s.Backend(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	if _, ok := b.(*Remote); !ok {
		t.Fatalf("backend is %T, want the server", b)
	}
	// With no server listening, the lock error is returned.
	s.Config.Serve.Addr = "127.0.0.1:1"
	if _, _, err := s.Backend(ctx); err == nil {
		t.Fatal("locked index with no server succeeded")
	}
	// An explicit server is used without touching the file.
	s.Config.Serve.URL = srv.URL
	if b, _, err := s.Backend(ctx); err != nil || b == nil {
		t.Fatalf("explicit server = %v", err)
	}
	// Other open errors are returned as they are.
	s.Config.Serve.URL = ""
	s.Config.DB = "/dev/null/x.duckdb"
	if _, _, err := s.Backend(ctx); err == nil {
		t.Fatal("bad db accepted")
	}
}
