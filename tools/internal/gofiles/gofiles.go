// Package gofiles walks the Go packages named by command-line patterns and
// parses their files, for the repo's check tools.
package gofiles

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"strings"
)

// File is one parsed Go source file.
type File struct {
	// ImportPath is the package's import path.
	ImportPath string
	// Path is the file's path on disk.
	Path string
	// AST is the parsed file, with comments.
	AST  *ast.File
	Fset *token.FileSet
	// Generated reports a "Code generated ... DO NOT EDIT." header.
	Generated bool
	// Test reports a _test.go file.
	Test bool
}

// Walk lists the packages matching patterns with go list and parses every
// Go file in them, test files included.
func Walk(patterns []string) ([]File, error) {
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	args := append([]string{"list", "-f", "{{.ImportPath}}\t{{.Dir}}\t{{join .GoFiles \",\"}},{{join .TestGoFiles \",\"}},{{join .XTestGoFiles \",\"}}"}, patterns...)
	out, err := exec.Command("go", args...).Output() // #nosec G204 -- fixed binary, patterns from our own Makefile.
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var files []File
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		for _, name := range strings.Split(parts[2], ",") {
			if name == "" {
				continue
			}
			path := filepath.Join(parts[1], name)
			f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				return nil, err
			}
			files = append(files, File{
				ImportPath: parts[0], Path: path, AST: f, Fset: fset,
				Generated: ast.IsGenerated(f), Test: strings.HasSuffix(name, "_test.go"),
			})
		}
	}
	return files, nil
}
