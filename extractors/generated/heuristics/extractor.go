// Package heuristics implements the generated.heuristics extractor
// (P2.T28 + P2.T30 + plan §5 risk-row mitigation).
//
// Detection rules (closed allowlists in common/common.go):
//   - sentinel + filename → confidence 0.99 GeneratedArtifact
//   - sentinel only       → confidence 0.85 GeneratedArtifact
//   - filename only       → UnverifiedGeneratedArtifact notice (no
//                            entity row — user confirms via manifest
//                            entry or by adding a header sentinel)
//   - neither             → no emission
package heuristics

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/shivamstaq/graph-harness/extractors/generated/common"
	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Name is the registry key for this extractor.
const Name = "generated.heuristics"

// Extractor implements code_framework.Extractor.
type Extractor struct {
	workspace string
	logf      func(string, ...any)
	// readHead lets tests inject a fake file reader. Production
	// path is realReadHead which opens the file under the workspace
	// root and reads up to N bytes.
	readHead func(absPath string) ([]byte, error)
}

// New constructs the heuristics extractor with the dispatcher-supplied
// deps. Used by the package init() registration.
func New(deps cf.Deps) (cf.Extractor, error) {
	e := &Extractor{
		workspace: deps.Workspace,
		logf:      deps.Logf,
	}
	if e.logf == nil {
		e.logf = func(string, ...any) {}
	}
	e.readHead = e.realReadHead
	return e, nil
}

// Name returns the registry key.
func (e *Extractor) Name() string { return Name }

// Inputs returns the kernel event kinds the extractor subscribes to.
// Only InputCoreFileChanged: removals do not create artifacts to
// detect, and the per-file payload is sufficient.
func (e *Extractor) Inputs() []cf.EventKind {
	return []cf.EventKind{cf.InputCoreFileChanged}
}

// Outputs returns the framework entity kinds this extractor emits.
func (e *Extractor) Outputs() []cf.EntityKind {
	return []cf.EntityKind{cf.KindGeneratedArtifact}
}

// Capabilities returns the static self-description.
func (e *Extractor) Capabilities() cf.Capabilities {
	return cf.Capabilities{
		Family:     "generated",
		Languages:  []string{"go", "typescript", "python"},
		Frameworks: []string{"protoc-gen-go", "stringer", "mockgen", "openapi-generator", "grpc-gateway", "go-generate"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
	}
}

// OnEvent inspects the changed file and emits zero or one of:
//   - GeneratedArtifactAdded (sentinel match, with or without filename match)
//   - UnverifiedGeneratedArtifact (filename match only, no sentinel)
//
// Errors during file read are logged and the event is skipped — a
// transient stat failure should not block the pipeline (compare-
// before-emit handles re-emission idempotently on next event).
func (e *Extractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	_ = ctx // no long-running work; ctx.Done() not consulted on a single read.
	relPath := common.FileChangedPath(in)
	if relPath == "" {
		return nil, nil
	}

	absPath := e.absPath(relPath)
	head, err := e.readHead(absPath)
	if err != nil {
		// File may have been removed between FileChanged and our
		// read; downgrade to a no-op rather than surface the error
		// to the dispatcher (which would bump the per-extractor
		// error counter for a transient race).
		e.logf("heuristics: read %s: %v", absPath, err)
		head = nil
	}

	res := common.Detect(relPath, head)
	switch {
	case res.SentinelGenerator != "" && res.MatchedFilename:
		// Sentinel + filename = confidence 0.99.
		// Prefer the sentinel-side generator label when both
		// matched; sentinel is the stronger signal.
		gen := res.SentinelGenerator
		_, ev := common.BuildGeneratedArtifact(
			relPath, gen, res.MatchedSentinel, 0.99, providedByName(),
		)
		return []kernel.Event{ev}, nil

	case res.SentinelGenerator != "":
		// Sentinel only = confidence 0.85.
		_, ev := common.BuildGeneratedArtifact(
			relPath, res.SentinelGenerator, res.MatchedSentinel, 0.85, providedByName(),
		)
		return []kernel.Event{ev}, nil

	case res.MatchedFilename:
		// Filename only → escape-hatch notice (plan §5 risk row).
		ev := common.BuildUnverifiedGeneratedArtifact(
			relPath, res.FilenameGenerator, providedByName(),
		)
		return []kernel.Event{ev}, nil
	}
	return nil, nil
}

// absPath resolves a workspace-relative path against the configured
// workspace root. If the path is already absolute, returns it as-is.
func (e *Extractor) absPath(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	if e.workspace == "" {
		return rel
	}
	return filepath.Join(e.workspace, rel)
}

// realReadHead opens absPath and returns the first MaxSentinelLines
// lines worth of bytes. We read a generous 16 KiB envelope to cover
// long shebang/preamble lines; the line-cap is enforced inside
// common.Detect.
func (e *Extractor) realReadHead(absPath string) ([]byte, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, 16*1024)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return buf[:n], nil
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
		Frameworks: []string{"protoc-gen-go", "stringer", "mockgen", "openapi-generator", "grpc-gateway", "go-generate"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
		Inputs:     []cf.EventKind{cf.InputCoreFileChanged},
		Outputs:    []cf.EntityKind{cf.KindGeneratedArtifact},
	}
}

func init() {
	cf.Register(Name, New, descriptor())
}
