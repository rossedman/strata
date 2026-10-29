// Command gendocs writes one Markdown reference page per strata command
// into docs/reference/commands, so the docs never drift from the CLI.
//
//	go run ./tools/gendocs [dir]
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra/doc"

	"github.com/rossedman/strata/cmd"
)

// main regenerates the command reference.
func main() {
	dir := filepath.Join("docs", "reference", "commands")
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	if err := run(dir); err != nil {
		fmt.Fprintln(os.Stderr, "gendocs:", err)
		os.Exit(1)
	}
}

// run clears dir and writes the pages.
func run(dir string) error {
	if err := os.RemoveAll(dir); err != nil { //nolint:gosec // a developer tool writing where it is told
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // a developer tool writing where it is told
		return err
	}
	root := cmd.Root()
	root.DisableAutoGenTag = true
	prepender := func(string) string { return "" }
	linker := func(name string) string { return strings.TrimSuffix(name, ".md") + ".md" }
	return doc.GenMarkdownTreeCustom(root, dir, prepender, linker)
}
