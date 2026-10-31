package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rossedman/strata/internal/config"
	"github.com/rossedman/strata/internal/ecr"
	"github.com/rossedman/strata/internal/fixture"
	"github.com/rossedman/strata/internal/store"
	"github.com/rossedman/strata/internal/testreg"
)

func TestIndexFilters(t *testing.T) {
	isolate(t)
	r := testreg.Start(t)
	db := filepath.Join(t.TempDir(), "filtered.duckdb")
	out := mustRun(t, "index", "--db", db, "--registry", r.Host, "--insecure",
		"--exclude-repo", "payments/worker", "--exclude-tag", "latest", "--platforms", "linux/amd64")
	contains(t, out, "3 repos", "filtered out 1 repos, 1 tags, 1 images", "requests per new image", "3 images indexed")

	for _, args := range [][]string{
		{"--platforms", "linux"},
		{"--exclude-tag", "[x"},
		{"--pushed-since", "90d"},
		{"--provider", "gcr"},
	} {
		if _, err := run(t, append([]string{"index", "--db", db, "--registry", r.Host, "--insecure"}, args...)...); err == nil {
			t.Errorf("index %v should fail", args)
		}
	}
}

func TestCrawlOptionsECR(t *testing.T) {
	isolate(t)
	mustRun(t, "version")
	const host = "123456789012.dkr.ecr.us-east-1.amazonaws.com"
	orig := newECR
	t.Cleanup(func() { newECR = orig })
	newECR = func(ctx context.Context, r config.Registry) (*ecr.Registry, error) {
		return ecr.NewWithAPI(ctx, r.Host, "123456789012", nil), nil
	}
	opts, err := crawlOptions(context.Background(), config.Registry{Host: host, PushedSince: "30d"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Lister == nil || opts.Keychain == nil || opts.Filter.PushedSince == 0 {
		t.Fatalf("ECR options = %+v", opts)
	}
	if opts, err := crawlOptions(context.Background(), config.Registry{Host: host, Provider: config.ProviderDistribution}); err != nil || opts.Lister != nil {
		t.Fatalf("provider distribution should use the plain API: %+v, %v", opts, err)
	}
	newECR = func(context.Context, config.Registry) (*ecr.Registry, error) { return nil, errors.New("no AWS config") }
	if _, err := crawlOptions(context.Background(), config.Registry{Host: host}); err == nil {
		t.Fatal("an ECR client error should fail")
	}
	if _, err := crawlOptions(context.Background(), config.Registry{Host: "reg.io", PushedSince: "nope"}); err == nil {
		t.Fatal("a bad filter should fail")
	}
}

func TestRegistryOptionsShareECRCredentials(t *testing.T) {
	isolate(t)
	mustRun(t, "version")
	orig := newECR
	t.Cleanup(func() { newECR = orig })
	var got []config.Registry
	newECR = func(ctx context.Context, r config.Registry) (*ecr.Registry, error) {
		got = append(got, r)
		return ecr.NewWithAPI(ctx, r.Host, "111111111111", nil), nil
	}
	regs := []config.Registry{
		{Host: "111111111111.dkr.ecr.us-east-1.amazonaws.com", AWSProfile: "bases"},
		{Host: "222222222222.dkr.ecr.us-east-1.amazonaws.com", AssumeRole: "arn:aws:iam::222222222222:role/read"},
		{Host: "reg.io"},
	}
	all, err := registryOptions(context.Background(), regs)
	if err != nil || len(all) != 3 {
		t.Fatalf("options = %v, %v", all, err)
	}
	if len(got) != 2 || got[0].AWSProfile != "bases" || got[1].AssumeRole == "" {
		t.Fatalf("ECR clients built with %+v", got)
	}
	for i, o := range all {
		if o.Keychain == nil {
			t.Errorf("registry %d has no keychain", i)
		}
	}
	newECR = func(context.Context, config.Registry) (*ecr.Registry, error) { return nil, errors.New("no profile") }
	if _, err := registryOptions(context.Background(), regs); err == nil || !strings.Contains(err.Error(), "111111111111") {
		t.Fatalf("err = %v", err)
	}
}

func TestCrawlOptionsAPIs(t *testing.T) {
	isolate(t)
	mustRun(t, "version")
	for _, r := range []config.Registry{
		{Host: "harbor.example.com", Provider: config.ProviderHarbor},
		{Host: "us-docker.pkg.dev", Projects: []string{"acme"}},
		{Host: "acme.azurecr.io"},
	} {
		opts, err := crawlOptions(context.Background(), r)
		if err != nil || opts.Lister == nil {
			t.Errorf("%s: options = %+v, %v", r.Host, opts, err)
		}
	}
	for _, r := range []config.Registry{
		{Host: "bad host!", Provider: config.ProviderHarbor},
		{Host: "bad host!", Provider: config.ProviderACR},
		{Host: "gcr.io", Provider: config.ProviderGAR},
	} {
		if _, err := crawlOptions(context.Background(), r); err == nil {
			t.Errorf("%s: bad host accepted", r.Host)
		}
	}
	tok := googleToken(context.Background(), "us-docker.pkg.dev")
	if _, err := tok(); err == nil {
		t.Log("Google credentials are available here; token lookup succeeded")
	}
	if _, err := googleToken(context.Background(), "bad host!")(); err == nil {
		t.Fatal("bad host accepted")
	}
}

func TestIndexFilterFlagsOnConfiguredRegistries(t *testing.T) {
	isolate(t)
	r := testreg.Start(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "strata.yaml")
	body := "db: " + filepath.Join(dir, "x.duckdb") + "\nregistries:\n  - host: " + r.Host + "\n    insecure: true\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out := mustRun(t, "index", "--config", cfg, "--include-repo", "base/**")
	contains(t, out, "2 repos", "filtered out 2 repos")
	if _, err := run(t, "index", "--config", cfg, "--pushed-since", "30d"); err == nil {
		t.Fatal("pushed-since on a plain registry should fail")
	}
}

func TestServeRefreshesUnbuiltIndex(t *testing.T) {
	isolate(t)
	db := filepath.Join(t.TempDir(), "unbuilt.duckdb")
	st, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write(context.Background(), fixture.Org(40)); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reset(rootCmd)
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"serve", "--db", db, "--addr", "127.0.0.1:0"})
	done := make(chan error, 1)
	go func() { done <- rootCmd.ExecuteContext(ctx) }()
	time.Sleep(3 * time.Second)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve: %v\n%s", err, out.String())
	}
	contains(t, out.String(), "computing lineage and staleness for the existing index")
	st, err = store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if v, err := st.Meta(context.Background(), "refreshed_at"); err != nil || v == "" {
		t.Fatalf("serve did not refresh the index: %q, %v\n%s", v, err, out.String())
	}
}
