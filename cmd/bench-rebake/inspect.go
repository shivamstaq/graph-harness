package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// inspectParsedFiles walks a fixture root and prints every parsed
// QualifiedName the polyglot indexer would materialize. Used
// interactively to debug fixture/overlay drift.
func inspectParsedFiles(root string) {
	if root == "" {
		return
	}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error { //nolint:gosec // operator-supplied root
		if err != nil {
			return nil
		}
		if d.IsDir() {
			n := d.Name()
			if strings.HasPrefix(n, ".") || n == "node_modules" || n == "__pycache__" {
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if source_live.LanguageOf(path) == "" {
			return nil
		}
		src, _ := os.ReadFile(path) //nolint:gosec
		rel, _ := filepath.Rel(root, path)
		pf, err := source_live.ParseFile(rel, src)
		if err != nil || pf == nil {
			return nil
		}
		fmt.Printf("%s [%s]\n", rel, pf.Language)
		for _, fn := range pf.Functions {
			fmt.Printf("  %s  recv=%q  body=%s\n", fn.QualifiedName, fn.Receiver, fn.BodyHash[:12])
		}
		return nil
	})
}
