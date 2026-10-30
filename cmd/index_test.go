package cmd

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/rossedman/strata/internal/config"
	"github.com/rossedman/strata/internal/ecr"
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
	newECR = func(ctx context.Context, h string) (*ecr.Registry, error) {
		return ecr.NewWithAPI(ctx, h, "123456789012", nil), nil
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
	newECR = func(context.Context, string) (*ecr.Registry, error) { return nil, errors.New("no AWS config") }
	if _, err := crawlOptions(context.Background(), config.Registry{Host: host}); err == nil {
		t.Fatal("an ECR client error should fail")
	}
	if _, err := crawlOptions(context.Background(), config.Registry{Host: "reg.io", PushedSince: "nope"}); err == nil {
		t.Fatal("a bad filter should fail")
	}
}
