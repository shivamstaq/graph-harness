// Package goose implements the Goose migrations extractor. Goose
// migration files look like `<n>_<name>.sql` or `<n>_<name>.go` where
// `<n>` is a numeric version (timestamp or sequence). SQL files carry
// `-- +goose Up` / `-- +goose Down` markers; Go files have
// `goose.AddMigration(...)` calls.
//
// We detect the migration by its filename shape + (for SQL) the
// `-- +goose Up` marker. Each migration file emits exactly one
// Migration row.
package goose

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/shivamstaq/graph-harness/extractors/schema/common"
	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Name is the registry identifier.
const Name = "migrations.goose"

type extractor struct {
	workspace string
	logf      func(string, ...any)
}

// New constructs an extractor instance.
func New(deps cf.Deps) (cf.Extractor, error) {
	return &extractor{workspace: deps.Workspace, logf: deps.Logf}, nil
}

func (e *extractor) Name() string                  { return Name }
func (e *extractor) Inputs() []cf.EventKind        { return []cf.EventKind{cf.InputCoreFileChanged} }
func (e *extractor) Outputs() []cf.EntityKind      { return []cf.EntityKind{cf.KindMigration} }
func (e *extractor) Capabilities() cf.Capabilities {
	return cf.Capabilities{
		Family:     "schemas",
		Languages:  []string{"go"},
		Frameworks: []string{"goose"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
	}
}

// gooseFilenameRE matches `<n>_<name>.sql|go` shaped basenames. The
// leading number can be a timestamp (14 digits) or a small sequence.
var gooseFilenameRE = regexp.MustCompile(`^(\d+)_([A-Za-z0-9_\-]+)\.(sql|go)$`)

// gooseMarkerRE finds `-- +goose Up` (case-insensitive) inside SQL.
var gooseMarkerRE = regexp.MustCompile(`(?i)--\s*\+goose\s+up`)

// gooseGoCallRE finds `goose.AddMigration` calls.
var gooseGoCallRE = regexp.MustCompile(`goose\.AddMigration`)

// OnEvent decides whether the changed file is a Goose migration and,
// if so, emits one MigrationAdded event.
func (e *extractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	_ = ctx
	path, err := common.FileFromEvent(in)
	if err != nil || path == "" {
		return nil, nil
	}
	return Parse(e.workspace, path), nil
}

// Parse is the test entrypoint. workspace is the root used to resolve
// the file's contents; passing "" reads from path directly.
func Parse(workspace, path string) []kernel.Event {
	base := filepath.Base(path)
	m := gooseFilenameRE.FindStringSubmatch(base)
	if m == nil {
		return nil
	}
	src, err := common.ReadFile(workspace, path)
	if err != nil {
		return nil
	}
	// SQL files must contain a Goose marker; Go files must contain
	// the AddMigration call. Otherwise treat as not-a-goose.
	switch strings.ToLower(filepath.Ext(base)) {
	case ".sql":
		if !gooseMarkerRE.Match(src) {
			return nil
		}
	case ".go":
		if !gooseGoCallRE.Match(src) {
			return nil
		}
	default:
		return nil
	}
	producedBy := common.ProducedBy(Name)
	anchor := common.PathAnchor(path)
	return []kernel.Event{common.EmitMigration(producedBy, "goose", m[1], anchor)}
}

func init() {
	cf.Register(Name, New, cf.Descriptor{
		Family:     "schemas",
		Languages:  []string{"go"},
		Frameworks: []string{"goose"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
		Inputs:     []cf.EventKind{cf.InputCoreFileChanged},
		Outputs:    []cf.EntityKind{cf.KindMigration},
	})
}
