// Package semantic_overlay implements the semantic.overlay layer importer
// and the multi-anchor selector resolver. SPEC §3, §11. The P1 resolver
// supports the full multi-anchor ladder with outcomes {bound, reanchored,
// unresolved}; the `ambiguous` and `superseded` outcomes remain P3-deferred.
package semantic_overlay

import (
	"context"
	"fmt"
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

// Resolve runs the qualified-name shortcut against the code.core store. This
// preserves the P0 single-anchor entry point while task P1.F (the
// multi-anchor ladder under internal/semantic_overlay/anchors/) is being
// brought up; once that lands, this method delegates to the ladder
// evaluator and only the qualified_name anchor still hits the inline path.
func (o *Overlay) Resolve(ctx context.Context, name string, store *code_core.Store, atSeq uint64) (*ResolutionEnvelope, error) {
	sel, ok := o.Selectors[name]
	if !ok {
		return nil, fmt.Errorf("selector %q not found in overlay", name)
	}
	env := &ResolutionEnvelope{
		SelectorID: sel.Name,
		Outcome:    OutcomeUnresolved,
		ResolvedAt: atSeq,
	}
	for _, anchor := range sel.Anchors {
		if anchor.Kind != "qualified_name" || anchor.Value == nil || anchor.Value.Str == nil {
			continue
		}
		// New P1 anchor value type is *AnchorValue; the qualified_name anchor
		// keeps its string payload under the same Str field.
		qn := *anchor.Value.Str
		ent, err := store.LookupByQualifiedName(ctx, qn)
		if err != nil {
			return nil, err
		}
		if ent != nil {
			env.Outcome = OutcomeBound
			env.Matches = append(env.Matches, ResolutionMatch{
				EntityID:      ent.ID,
				QualifiedName: ent.QualifiedName,
				Confidence:    0.97, // qualified_name exact match (anchor-ladder default for the nominal anchor)
				ViaAnchor:     "qualified_name",
			})
			break
		}
	}
	return env, nil
}
