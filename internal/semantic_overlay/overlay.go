// Package semantic_overlay implements the semantic.overlay layer importer
// and the multi-anchor selector resolver. SPEC §3, §11. The P1 resolver
// supports the full multi-anchor ladder with outcomes {bound, reanchored,
// unresolved}; the `ambiguous` and `superseded` outcomes remain P3-deferred.
package semantic_overlay

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/dsl"
)

// Overlay is the parsed, in-memory view of every .gh file in the workspace.
// SPEC §4.8: the .gh files on disk are canonical; the overlay index is
// rebuildable from source.
type Overlay struct {
	Selectors map[string]*dsl.Selector
	Flows     map[string]*dsl.Flow
}

// NewOverlay returns an empty overlay.
func NewOverlay() *Overlay {
	return &Overlay{
		Selectors: map[string]*dsl.Selector{},
		Flows:     map[string]*dsl.Flow{},
	}
}

// Load walks the overlay directory (.graph-harness/overlay/**) and parses
// every .gh file into the overlay. Errors are returned per-file via the
// returned error map; the overlay still contains everything that parsed.
func (o *Overlay) Load(overlayDir string) (map[string]error, error) {
	errs := map[string]error{}
	if _, err := os.Stat(overlayDir); os.IsNotExist(err) {
		return errs, nil // no overlay yet is fine
	}
	walkErr := filepath.WalkDir(overlayDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".gh") {
			return nil
		}
		// #nosec G304,G122 -- overlay directory is operator-controlled and rooted.
		data, err := os.ReadFile(path) //nolint:gosec
		if err != nil {
			errs[path] = err
			return nil
		}
		f, err := dsl.ParseString(path, string(data))
		if err != nil {
			errs[path] = err
			return nil
		}
		for _, decl := range f.Decls {
			switch {
			case decl.Selector != nil:
				o.Selectors[decl.Selector.Name] = decl.Selector
			case decl.Flow != nil:
				o.Flows[decl.Flow.Name] = decl.Flow
			}
		}
		return nil
	})
	return errs, walkErr
}

// ResolutionOutcome enumerates the SPEC §3.2 outcomes. P1 supports the
// `bound`, `reanchored`, and `unresolved` subset; `ambiguous` and `superseded`
// remain deferred to P3.
type ResolutionOutcome string

// SPEC §3.2 resolution outcomes (P1-active subset).
const (
	OutcomeBound      ResolutionOutcome = "bound"
	OutcomeReanchored ResolutionOutcome = "reanchored"
	OutcomeUnresolved ResolutionOutcome = "unresolved"
)

// ResolutionMatch is one resolved entity reference.
type ResolutionMatch struct {
	EntityID      string  `json:"entity_id"`
	QualifiedName string  `json:"qualified_name"`
	Confidence    float64 `json:"confidence"`
	ViaAnchor     string  `json:"via_anchor"`
}

// ResolutionEnvelope is the structured result returned by selector resolution
// (SPEC §3.2). The drift-signals slice and full ambiguous-set extras land
// alongside the `ambiguous` outcome in P3.
type ResolutionEnvelope struct {
	SelectorID string            `json:"selector_id"`
	Outcome    ResolutionOutcome `json:"outcome"`
	Matches    []ResolutionMatch `json:"matches"`
	ResolvedAt uint64            `json:"resolved_at_kernel_seq"`
}

// Resolve runs the multi-anchor selector ladder (SPEC §3.3) against the
// code.core store. The returned envelope follows SPEC §3.2; per-anchor
// trace data is available via [ResolveWithTrace] for `--explain`.
//
// This method constructs a fresh in-memory resolver per call. Long-running
// callers (daemon, JSON-RPC service) should construct a [Resolver] with
// [NewResolver] once, wire drift-event invalidation via
// [Resolver.WireDriftInvalidation], and reuse across calls — that way the
// SPEC §6.13 selector_resolution_cache stays warm and lazy-invalidates on
// SymbolMoved / SymbolRenamed / SignatureChanged / SymbolDeleted events.
func (o *Overlay) Resolve(ctx context.Context, name string, store *code_core.Store, atSeq uint64) (*ResolutionEnvelope, error) {
	env, _, err := o.ResolveWithTrace(ctx, name, store, atSeq)
	if err != nil {
		return env, err
	}
	// Reverse-index hookup (P2.T35a / P2.M03): for each match in a
	// `bound` (or future `reanchored`) outcome, write a row to
	// entity_selector_index so hover surfaces can answer
	// "entity → selectors" in O(1). Best-effort: log on failure, do
	// not fail the resolve. The resolver is synchronous per-call,
	// no new goroutines.
	if env != nil && env.Outcome == OutcomeBound {
		for _, m := range env.Matches {
			if err := store.BindSelector(ctx, m.EntityID, name, "", m.ViaAnchor, atSeq); err != nil {
				log.Printf("semantic_overlay: BindSelector(%s,%s) failed: %v", m.EntityID, name, err)
			}
		}
	}
	return env, nil
}

// ResolveWithTrace is like [Resolve] but also returns the per-anchor
// evaluation trace used by `graph-harness selectors test --explain` to
// describe which anchor matched and why each lower-priority anchor was
// not consulted.
func (o *Overlay) ResolveWithTrace(ctx context.Context, name string, store *code_core.Store, atSeq uint64) (*ResolutionEnvelope, []AnchorTrace, error) {
	r, err := NewResolver(o, store, nil)
	if err != nil {
		return nil, nil, err
	}
	return r.Resolve(ctx, name, atSeq)
}
