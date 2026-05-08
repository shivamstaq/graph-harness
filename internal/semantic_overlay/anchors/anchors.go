// Package anchors implements per-anchor evaluators that the multi-anchor
// selector resolver (SPEC §3.3) walks top-down. Each evaluator answers a
// single question for a single anchor kind: "given this anchor value, which
// code.core entities does it reference, and how confident are we?". The
// resolver picks the first anchor whose best score crosses the per-outcome
// threshold; lower-priority anchors only execute if higher ones miss.
//
// Design contract:
//
//   - Evaluators must be pure: read-only against Lookup, no side effects on
//     the store, no event emission.
//   - Confidence scores are anchor-kind-specific defaults reflecting
//     evidentiary strength: nominal anchors score highest because a name
//     match is strong evidence; structural anchors (path_glob) score lowest
//     because a glob can match many candidates.
//   - Evaluators that require data not present in the store (e.g. a
//     symbol_fingerprint column populated by a future extractor) return
//     zero matches, never an error. The resolver treats that as "this
//     anchor did not fire" and falls through.
package anchors

import (
	"context"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/dsl"
)

// Match is one resolved entity reference together with the evaluator's
// per-anchor confidence. Higher is more confident, in [0, 1].
type Match struct {
	EntityID      string  `json:"entity_id"`
	QualifiedName string  `json:"qualified_name"`
	LanguageID    string  `json:"language_id"`
	Confidence    float64 `json:"confidence"`
	Detail        string  `json:"detail,omitempty"`
}

// Lookup is the minimal store API every evaluator depends on. The concrete
// type wired in production is *code_core.Store; tests inject a fake.
type Lookup interface {
	LookupByQualifiedName(ctx context.Context, qn string) (*code_core.Entity, error)
	LookupAllByQualifiedName(ctx context.Context, qn string) ([]code_core.Entity, error)
	LookupByBodyHash(ctx context.Context, bh string) ([]code_core.Entity, error)
	LookupBySymbolFingerprint(ctx context.Context, fp string) ([]code_core.Entity, error)
	LookupByASTHash(ctx context.Context, h string) ([]code_core.Entity, error)
	LookupFunctions(ctx context.Context) ([]code_core.Entity, error)
	LookupAllEntities(ctx context.Context) ([]code_core.Entity, error)
	CallerNames(ctx context.Context, toID string) ([]string, error)
	CalleeNames(ctx context.Context, fromID string) ([]string, error)
}

// Evaluator evaluates one anchor against the store and returns ranked matches.
type Evaluator interface {
	// Kind is the anchor.Kind string this evaluator handles.
	Kind() string
	// Evaluate returns matches sorted by Confidence descending.
	Evaluate(ctx context.Context, anchor *dsl.Anchor, store Lookup) ([]Match, error)
}

// Default per-anchor confidence scores. The numbers are calibrated against
// the SPEC §3.3 example thresholds (`bound: 0.95`, `reanchored: 0.75`):
//
//	qualified_name (exact match)            → 0.97  → bound
//	body_hash (exact match, language-blind) → 0.95  → bound
//	function_signature (sig match)          → 0.85  → reanchored
//	symbol_fingerprint, ast_hash            → 0.93  → reanchored
//	call_neighborhood (1+ caller AND callee)→ 0.80  → reanchored
//	path_glob (path match)                  → 0.55  → below reanchored alone
const (
	ConfidenceQualifiedName     = 0.97
	ConfidenceBodyHash          = 0.95
	ConfidenceFunctionSig       = 0.85
	ConfidenceSymbolFingerprint = 0.93
	ConfidenceASTHash           = 0.93
	ConfidenceCallNeighborBoth  = 0.80
	ConfidenceCallNeighborOne   = 0.65
	ConfidencePathGlob          = 0.55
	// language_id alone is a weak discriminator (many entities share a
	// language); it scores below the default reanchored threshold so it
	// only contributes when paired with a higher-precision anchor on the
	// same selector.
	ConfidenceLanguageID = 0.50
)

// Registry returns the default evaluator set. The order is the canonical
// authoring ladder used when a selector omits some anchors — a selector
// with all kinds present is evaluated in the author's declared order, but
// utilities (e.g. --explain output) reuse this default for display.
func Registry() map[string]Evaluator {
	return map[string]Evaluator{
		"qualified_name":     QualifiedName{},
		"body_hash":          BodyHash{},
		"function_signature": FunctionSignature{},
		"symbol_fingerprint": SymbolFingerprint{},
		"ast_hash":           ASTHash{},
		"call_neighborhood":  CallNeighborhood{},
		"path_glob":          PathGlob{},
		"language_id":        LanguageID{},
	}
}

// Evaluate routes an anchor to its evaluator. Unknown kinds return no
// matches with no error; the resolver treats that as a cleanly-skipped
// rung on the ladder.
func Evaluate(ctx context.Context, anchor *dsl.Anchor, store Lookup) ([]Match, error) {
	if anchor == nil {
		return nil, nil
	}
	ev, ok := Registry()[anchor.Kind]
	if !ok {
		return nil, nil
	}
	return ev.Evaluate(ctx, anchor, store)
}

// stringValue is the common helper for anchors carrying a single string.
func stringValue(a *dsl.Anchor) string {
	if a == nil || a.Value == nil || a.Value.Str == nil {
		return ""
	}
	return *a.Value.Str
}

// matchFromEntity is the canonical Match builder.
func matchFromEntity(e code_core.Entity, conf float64, detail string) Match {
	return Match{
		EntityID:      e.ID,
		QualifiedName: e.QualifiedName,
		LanguageID:    e.LanguageID,
		Confidence:    conf,
		Detail:        detail,
	}
}
