package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "commands")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "strata_removed.md")
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale page kept")
	}
	page, err := os.ReadFile(filepath.Join(dir, "strata_query.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "strata query") || strings.Contains(string(page), "Auto generated") {
		t.Fatalf("query page:\n%s", page)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(filepath.Join(blocker, "x")); err == nil {
		t.Fatal("run under a file succeeded")
	}
}
