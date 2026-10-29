// Package testenv builds an indexed test registry for tests of the
// interfaces on top of the service: the API, MCP server, CLI and commands.
package testenv

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"

	"github.com/rossedman/strata/internal/crawl"
	"github.com/rossedman/strata/internal/service"
	"github.com/rossedman/strata/internal/store"
	"github.com/rossedman/strata/internal/testreg"
)

// Env is a test registry and an index of it.
type Env struct {
	Reg *testreg.Registry
	Svc *service.Service
	// DB is the index file, for tests that open it themselves.
	DB string
}

// Indexed starts a test registry and indexes it into a temp file, kept
// open for the test's lifetime.
func Indexed(t testing.TB) *Env {
	t.Helper()
	e := IndexedFile(t)
	st, err := store.Open(context.Background(), e.DB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e.Svc = service.New(st)
	return e
}

// IndexedFile indexes a test registry into a temp file and closes it, so
// a command under test can open it.
func IndexedFile(t testing.TB) *Env {
	t.Helper()
	r := testreg.Start(t)
	db := filepath.Join(t.TempDir(), "strata.duckdb")
	st, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	svc := service.New(st)
	if _, err := svc.Index(context.Background(), Options(r)); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return &Env{Reg: r, DB: db}
}

// Options returns crawl options for a test registry.
func Options(r *testreg.Registry) crawl.Options {
	return crawl.Options{Registry: r.Host, Insecure: true, Referrers: true, Keychain: authn.NewMultiKeychain()}
}
