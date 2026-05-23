// Package alembic implements the Alembic migrations extractor.
// Alembic puts migration scripts under `migrations/versions/<rev>_<name>.py`
// with `def upgrade()` and `def downgrade()` functions, plus a
// `revision` top-level variable. We detect by path and by the
// revision marker.
package alembic

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
const Name = "migrations.alembic"

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
		Languages:  []string{"python"},
		Frameworks: []string{"alembic"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
	}
}

// revisionRE captures the alembic revision identifier from the
// top-level `revision = "..."` (or single-quoted) assignment.
var revisionRE = regexp.MustCompile(`(?m)^\s*revision\s*(?::\s*str\s*)?=\s*["']([^"']+)["']`)

// filenameRE matches a typical alembic filename `<rev>_<name>.py`
// where <rev> is hex-ish or any alnum identifier.
var filenameRE = regexp.MustCompile(`^([A-Za-z0-9]+)_([A-Za-z0-9_\-]+)\.py$`)

// OnEvent checks whether the file is in a `versions/` folder and
// looks like an alembic migration script.
func (e *extractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	_ = ctx
	path, err := common.FileFromEvent(in)
	if err != nil || path == "" {
		return nil, nil
	}
	return Parse(e.workspace, path), nil
}

// Parse is the test entrypoint.
func Parse(workspace, path string) []kernel.Event {
	if !strings.HasSuffix(strings.ToLower(path), ".py") {
		return nil
	}
	// Path must contain a "versions/" segment (case-insensitive on
	// Windows-friendly /); this is the canonical alembic layout.
	if !strings.Contains(filepath.ToSlash(strings.ToLower(path)), "versions/") {
		return nil
	}
	base := filepath.Base(path)
	m := filenameRE.FindStringSubmatch(base)
	if m == nil {
		return nil
	}
	src, err := common.ReadFile(workspace, path)
	if err != nil {
		return nil
	}
	rev := m[1]
	if rm := revisionRE.FindSubmatch(src); rm != nil {
		rev = string(rm[1])
	} else if !strings.Contains(string(src), "def upgrade") {
		// Without either marker, this is not an alembic file.
		return nil
	}
	producedBy := common.ProducedBy(Name)
	anchor := common.PathAnchor(path)
	return []kernel.Event{common.EmitMigration(producedBy, "alembic", rev, anchor)}
}

func init() {
	cf.Register(Name, New, cf.Descriptor{
		Family:     "schemas",
		Languages:  []string{"python"},
		Frameworks: []string{"alembic"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
		Inputs:     []cf.EventKind{cf.InputCoreFileChanged},
		Outputs:    []cf.EntityKind{cf.KindMigration},
	})
}
