package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestList(t *testing.T) {
	var b bytes.Buffer
	if err := list(&b, []string{"github.com/rossedman/strata/internal/api", "github.com/rossedman/strata/internal/chain"}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "internal/api/server.gen.go:") || strings.Contains(out, "chain.go") {
		t.Fatalf("list = %s", out)
	}
	if err := list(&b, []string{"./does/not/exist"}); err == nil {
		t.Fatal("bad pattern accepted")
	}
}
