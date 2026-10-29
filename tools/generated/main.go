// Command generated prints the import-path-qualified names of generated Go
// files (those with a "Code generated ... DO NOT EDIT." header), one per
// line, so coverage can be measured on hand-written code only:
//
//	grep -vFf <(go run ./tools/generated ./...) coverage.out
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rossedman/strata/tools/internal/gofiles"
)

// main prints generated file names.
func main() {
	if err := list(os.Stdout, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "generated:", err)
		os.Exit(2)
	}
}

// list writes the generated files in the packages matching patterns.
func list(w io.Writer, patterns []string) error {
	files, err := gofiles.Walk(patterns)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.Generated && !f.Test {
			if _, err := fmt.Fprintln(w, f.ImportPath+"/"+filepath.Base(f.Path)+":"); err != nil {
				return err
			}
		}
	}
	return nil
}
