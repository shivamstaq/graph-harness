// Package manifest implements the generated.manifest extractor
// (P2.T29). It reads workspace-declared glob patterns from
// .graph-harness/extractors.toml::generated_patterns and emits a
// GeneratedArtifact at confidence 1.0 for any file that matches.
//
// The manifest is authoritative — the user has explicitly named the
// path as a generator output — so manifest-matched files emit a
// regular GeneratedArtifact, never UnverifiedGeneratedArtifact.
package manifest

import (
	"context"
	"fmt"
	"sync"

	"github.com/shivamstaq/graph-harness/extractors/generated/common"
	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Name is the registry key for this extractor.
const Name = "generated.manifest"

// Extractor implements code_framework.Extractor.
type Extractor struct {
	workspace string
	logf      func(string, ...any)

	mu       sync.RWMutex
	patterns []string

	// loadConfig is overridable for tests so they don't have to
	// scaffold a .graph-harness/extractors.toml on disk.
	loadConfig func() ([]string, error)
}

// New constructs the manifest extractor. It reads the workspace's
// extractors.toml once at construction; subsequent updates require
// a Dispatcher restart (Pass 0's lifecycle model — runtime config
// reload lands later).
func New(deps cf.Deps) (cf.Extractor, error) {
	e := &Extractor{
		workspace: deps.Workspace,
		logf:      deps.Logf,
	}
	if e.logf == nil {
		e.logf = func(string, ...any) {}
	}
	e.loadConfig = e.realLoadConfig
	if err := e.refresh(); err != nil {
		// Surface the error: a malformed extractors.toml is a
		// config-time issue users should see, not silently swallow.
		return nil, fmt.Errorf("%s: %w", Name, err)
	}
	return e, nil
}

// Name returns the registry key.
func (e *Extractor) Name() string { return Name }

// Inputs returns the kernel event kinds this extractor subscribes to.
func (e *Extractor) Inputs() []cf.EventKind {
	return []cf.EventKind{cf.InputCoreFileChanged}
}

// Outputs returns the framework entity kinds emitted.
func (e *Extractor) Outputs() []cf.EntityKind {
	return []cf.EntityKind{cf.KindGeneratedArtifact}
}

// Capabilities returns the static self-description.
func (e *Extractor) Capabilities() cf.Capabilities {
	return cf.Capabilities{
		Family:     "generated",
		Languages:  []string{"go", "typescript", "python"},
		Frameworks: []string{"workspace-manifest"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
	}
}

// OnEvent checks the changed file's path against the declared globs;
// emits a confidence-1.0 GeneratedArtifact on match.
func (e *Extractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	_ = ctx
	relPath := common.FileChangedPath(in)
	if relPath == "" {
		return nil, nil
	}

	e.mu.RLock()
	patterns := e.patterns
	e.mu.RUnlock()
	if len(patterns) == 0 {
		return nil, nil
	}

	matched, glob := common.MatchesAnyGlob(relPath, patterns)
	if !matched {
		return nil, nil
	}

	// Sentinel field carries the matching glob — gives consumers a
	// human-readable "why" without re-running detection.
	_, ev := common.BuildGeneratedArtifact(
		relPath,
		"manifest",
		"manifest:"+glob,
		1.0,
		providedByName(),
	)
	return []kernel.Event{ev}, nil
}

// refresh reloads the patterns slice from the workspace config.
// Called once at New(); exposed for tests to swap loadConfig.
func (e *Extractor) refresh() error {
	patterns, err := e.loadConfig()
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.patterns = patterns
	e.mu.Unlock()
	return nil
}

// realLoadConfig reads .graph-harness/extractors.toml from the
// workspace root. Returns an empty slice (not an error) when no
// workspace is configured or the file is absent.
func (e *Extractor) realLoadConfig() ([]string, error) {
	if e.workspace == "" {
		return nil, nil
	}
	cfg, err := cf.LoadConfig(e.workspace)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}
	return cfg.GeneratedPatterns, nil
}

// providedByName returns the ProducedBy attribution string.
func providedByName() string {
	return fmt.Sprintf("%s:%s", cf.SourceExtractorFramework, Name)
}

// descriptor is the static metadata surfaced to `extractors list`
// without instantiating the extractor.
func descriptor() cf.Descriptor {
	return cf.Descriptor{
		Name:       Name,
		Family:     "generated",
		Languages:  []string{"go", "typescript", "python"},
		Frameworks: []string{"workspace-manifest"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
		Inputs:     []cf.EventKind{cf.InputCoreFileChanged},
		Outputs:    []cf.EntityKind{cf.KindGeneratedArtifact},
	}
}

func init() {
	cf.Register(Name, New, descriptor())
}
