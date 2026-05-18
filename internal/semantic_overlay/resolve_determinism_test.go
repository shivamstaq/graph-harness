package semantic_overlay

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/code_core"
)

// TestResolve_DeterministicOver100Runs closes the P0 gate-4
// invariant: the resolver's outcome envelope is byte-identical
// across 100 repeated runs against the same store at the same seq.
// Determinism is what lets surfaces (TUI, IDE code lens, validate-
// diff findings) cache resolver output keyed by (selector_id, seq).
func TestResolve_DeterministicOver100Runs(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	putFn(t, store, code_core.Entity{
		ID: "e1", Kind: code_core.KindFunction, LanguageID: "go",
		QualifiedName: "checkout.Validator.Validate",
		BodyHash:      "bh-validate",
	})
	putFn(t, store, code_core.Entity{
		ID: "e2", Kind: code_core.KindFunction, LanguageID: "go",
		QualifiedName: "checkout.Validator.Authorize",
		BodyHash:      "bh-authorize",
	})
	putFn(t, store, code_core.Entity{
		ID: "e3", Kind: code_core.KindFunction, LanguageID: "go",
		QualifiedName: "checkout.Validator.Validate",
		BodyHash:      "bh-different",
	})

	sel := parseSelectorOrFail(t, `selector CheckoutValidator {
  anchor qualified_name "checkout.Validator.Validate"
  anchor body_hash       "bh-validate"
  anchor path_glob       "internal/checkout/*.go"
}`)
	overlay := newOverlayWithSelector(sel)

	ref, refTrace, err := overlay.ResolveWithTrace(context.Background(), "CheckoutValidator", store, 100)
	if err != nil {
		t.Fatalf("ResolveWithTrace ref: %v", err)
	}
	refBuf, err := json.Marshal(envelopeShape(ref, refTrace))
	if err != nil {
		t.Fatalf("marshal ref: %v", err)
	}
	for i := 1; i < 100; i++ {
		env, trace, err := overlay.ResolveWithTrace(context.Background(), "CheckoutValidator", store, 100)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		buf, err := json.Marshal(envelopeShape(env, trace))
		if err != nil {
			t.Fatalf("iter %d marshal: %v", i, err)
		}
		if string(buf) != string(refBuf) {
			t.Fatalf("iter %d diverged:\n  ref:  %s\n  iter: %s", i, refBuf, buf)
		}
	}
}

func envelopeShape(env *ResolutionEnvelope, trace []AnchorTrace) any {
	return struct {
		Envelope *ResolutionEnvelope `json:"envelope"`
		Trace    []AnchorTrace       `json:"trace"`
	}{env, SortedTrace(trace)}
}
