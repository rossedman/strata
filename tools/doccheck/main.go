// Command doccheck fails if any function or method in the given packages
// lacks a doc comment, or any package lacks a package comment. Test files
// and generated files are skipped.
//
//	go run ./tools/doccheck ./...
package main

import (
	"fmt"
	"go/ast"
	"os"
	"sort"

	"github.com/rossedman/strata/tools/internal/gofiles"
)

// main reports undocumented functions and exits non-zero if any exist.
func main() {
	files, err := gofiles.Walk(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "doccheck:", err)
		os.Exit(2)
	}
	problems := check(files)
	for _, p := range problems {
		fmt.Println(p)
	}
	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "doccheck: %d missing doc comments\n", len(problems))
		os.Exit(1)
	}
}

// check returns one line per missing doc comment.
func check(files []gofiles.File) []string {
	var out []string
	pkgDoc := map[string]bool{}
	pkgSeen := map[string]string{}
	for _, f := range files {
		if f.Test || f.Generated {
			continue
		}
		pkgSeen[f.ImportPath] = f.Path
		if f.AST.Doc != nil {
			pkgDoc[f.ImportPath] = true
		}
		for _, d := range f.AST.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Doc != nil {
				continue
			}
			name := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				name = recvName(fn.Recv.List[0].Type) + "." + name
			}
			out = append(out, fmt.Sprintf("%s: %s has no doc comment", f.Fset.Position(fn.Pos()), name))
		}
	}
	for pkg, path := range pkgSeen {
		if !pkgDoc[pkg] {
			out = append(out, fmt.Sprintf("%s: package %s has no package comment", path, pkg))
		}
	}
	sort.Strings(out)
	return out
}

// recvName returns a method receiver's type name.
func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.IndexExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}
