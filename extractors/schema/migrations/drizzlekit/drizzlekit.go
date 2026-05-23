// Package drizzlekit implements the Drizzle Kit migrations extractor.
// Drizzle Kit emits flat SQL files under `drizzle/<n>_<name>.sql` (and
// a sidecar `meta/` folder we ignore). The filename's leading digit
// run is the Version.
package drizzlekit

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
const Name = "migrations.drizzlekit"

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
		Languages:  []string{"typescript"},
		Frameworks: []string{"drizzle-kit"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
	}
}

// filenameRE matches `<n>_<name>.sql`.
var filenameRE = regexp.MustCompile(`^(\d+)_([A-Za-z0-9_\-]+)\.sql$`)

// OnEvent decides whether the changed file is a Drizzle Kit migration.
func (e *extractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	_ = ctx
	path, err := common.FileFromEvent(in)
	if err != nil || path == "" {
		return nil, nil
	}
	return Parse(path), nil
}

// Parse is the test entrypoint.
func Parse(path string) []kernel.Event {
	p := filepath.ToSlash(path)
	if !strings.Contains(p, "drizzle/") {
		return nil
	}
	// Skip drizzle/meta/* — those are tooling-state files, not the
	// migration SQL itself.
	if strings.Contains(p, "/meta/") {
		return nil
	}
	base := filepath.Base(p)
	m := filenameRE.FindStringSubmatch(base)
	if m == nil {
		return nil
	}
	producedBy := common.ProducedBy(Name)
	anchor := common.PathAnchor(path)
	return []kernel.Event{common.EmitMigration(producedBy, "drizzle-kit", m[1], anchor)}
}

func init() {
	cf.Register(Name, New, cf.Descriptor{
		Family:     "schemas",
		Languages:  []string{"typescript"},
		Frameworks: []string{"drizzle-kit"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
		Inputs:     []cf.EventKind{cf.InputCoreFileChanged},
		Outputs:    []cf.EntityKind{cf.KindMigration},
	})
}
