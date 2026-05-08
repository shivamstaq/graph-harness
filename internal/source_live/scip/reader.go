package scip

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/shivamstaq/graph-harness/internal/source_live/scip/proto"
)

// Read parses a SCIP index file at path and returns the decoded Index.
// Returns a wrapped error if the file is unreadable or malformed.
func Read(path string) (*proto.Index, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", path, err)
	}
	body, err := os.ReadFile(abs) //nolint:gosec // index path comes from caller-controlled config
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	idx, err := proto.DecodeIndex(body)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return idx, nil
}

// FindIndexes returns every `.scip` file under root/.scip-index/. The
// `.scip-index/` directory is the conventional location used by
// scip-go / scip-typescript / scip-python; if a workspace uses a
// different convention, callers can pass a different `dir` to
// FindIndexesIn.
func FindIndexes(root string) ([]string, error) {
	return FindIndexesIn(filepath.Join(root, ".scip-index"))
}

// FindIndexesIn walks dir and returns every `.scip` file beneath it.
// Returns (nil, nil) if dir does not exist — a workspace without any
// SCIP indexes degrades gracefully to LSP + tree-sitter only.
func FindIndexesIn(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scip: %s is not a directory", dir)
	}
	var out []string
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".scip" {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, err)
	}
	return out, nil
}
