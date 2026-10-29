package main

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/rossedman/strata/tools/internal/gofiles"
)

// parse builds a gofiles.File from source.
func parse(t *testing.T, src string, generated bool) gofiles.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	return gofiles.File{ImportPath: "example.com/x", Path: "x.go", AST: f, Fset: fset, Generated: generated}
}

func TestCheck(t *testing.T) {
	src := `// Package x is documented.
package x

// Good is documented.
func Good() {}

func bad() {}

type T struct{}

func (t *T) Method() {}

type G[K any] struct{}

func (g G[K]) Generic() {}
`
	got := strings.Join(check([]gofiles.File{parse(t, src, false)}), "\n")
	for _, want := range []string{"bad has no doc comment", "T.Method has no doc comment", "G.Generic has no doc comment"} {
		if !strings.Contains(got, want) {
			t.Errorf("check lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Good") {
		t.Fatalf("documented function reported:\n%s", got)
	}
	noPkg := parse(t, "package y\n\n// F is documented.\nfunc F() {}\n", false)
	noPkg.ImportPath = "example.com/y"
	if got := check([]gofiles.File{noPkg}); len(got) != 1 || !strings.Contains(got[0], "no package comment") {
		t.Fatalf("package comment check = %v", got)
	}
	if got := check([]gofiles.File{parse(t, "package z\nfunc f() {}\n", true)}); len(got) != 0 {
		t.Fatalf("generated file checked: %v", got)
	}
}

func TestRepoIsDocumented(t *testing.T) {
	files, err := gofiles.Walk([]string{"github.com/rossedman/strata/..."})
	if err != nil {
		t.Fatal(err)
	}
	if problems := check(files); len(problems) > 0 {
		t.Fatalf("missing doc comments:\n%s", strings.Join(problems, "\n"))
	}
}
