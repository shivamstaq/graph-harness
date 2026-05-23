// Package prismamigrate implements the Prisma Migrate extractor.
// Prisma Migrate stores each migration as
// `prisma/migrations/<timestamp>_<name>/migration.sql`. Each such
// file emits one Migration row, with `<timestamp>` taken as the
// Version.
package prismamigrate

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
const Name = "migrations.prismamigrate"

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
		Languages:  []string{"typescript", "python", "go"},
		Frameworks: []string{"prisma-migrate"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
	}
}

// migrationDirRE matches the parent directory name
// `<timestamp>_<name>` typical of Prisma Migrate. The version is the
// leading digit run.
var migrationDirRE = regexp.MustCompile(`^(\d+)_([A-Za-z0-9_\-]+)$`)

// OnEvent decides whether this is a Prisma Migrate file.
func (e *extractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	_ = ctx
	path, err := common.FileFromEvent(in)
	if err != nil || path == "" {
		return nil, nil
	}
	return Parse(path), nil
}

// Parse is the test entrypoint. It does not need to read the file —
// the migration's identity comes from the path layout alone.
func Parse(path string) []kernel.Event {
	p := filepath.ToSlash(path)
	// Accept both "/migrations/" mid-path and a leading "migrations/" —
	// the layout the test fixtures use.
	if !strings.Contains(p, "/migrations/") && !strings.HasPrefix(p, "migrations/") {
		return nil
	}
	if filepath.Base(p) != "migration.sql" {
		return nil
	}
	// Parent directory shape: prisma/migrations/<ts>_<name>/migration.sql
	dirBase := filepath.Base(filepath.Dir(p))
	m := migrationDirRE.FindStringSubmatch(dirBase)
	if m == nil {
		return nil
	}
	producedBy := common.ProducedBy(Name)
	anchor := common.PathAnchor(path)
	return []kernel.Event{common.EmitMigration(producedBy, "prisma-migrate", m[1], anchor)}
}

func init() {
	cf.Register(Name, New, cf.Descriptor{
		Family:     "schemas",
		Languages:  []string{"typescript", "python", "go"},
		Frameworks: []string{"prisma-migrate"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
		Inputs:     []cf.EventKind{cf.InputCoreFileChanged},
		Outputs:    []cf.EntityKind{cf.KindMigration},
	})
}
