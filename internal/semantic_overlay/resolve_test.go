package semantic_overlay

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/dsl"
)

func openTestStore(t *testing.T) *code_core.Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := code_core.NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func putFn(t *testing.T, store *code_core.Store, e code_core.Entity) {
	t.Helper()
	if err := store.PutEntity(context.Background(), e, 1); err != nil {
		t.Fatalf("PutEntity: %v", err)
	}
}

// parseSelectorOrFail is a tiny helper that compiles a one-selector .gh
// snippet and returns the parsed Selector struct.
func parseSelectorOrFail(t *testing.T, src string) *dsl.Selector {
	t.Helper()
	f, err := dsl.ParseString("test.gh", src)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	if len(f.Decls) == 0 || f.Decls[0].Selector == nil {
		t.Fatalf("expected one selector decl, got %+v", f.Decls)
	}
	return f.Decls[0].Selector
}

func newOverlayWithSelector(sel *dsl.Selector) *Overlay {
	o := NewOverlay()
	o.Selectors[sel.Name] = sel
	return o
}

func TestResolve_QualifiedNameBindsAtBoundConfidence(t *testing.T) {
	store := openTestStore(t)
	putFn(t, store, code_core.Entity{
		ID: "f1", Kind: code_core.KindFunction, LanguageID: "go",
		QualifiedName: "payments.PaymentService.authorize",
	})
	sel := parseSelectorOrFail(t, `selector PaymentAuth {
		unique
		anchor qualified_name "payments.PaymentService.authorize"
	}`)
	overlay := newOverlayWithSelector(sel)
	env, trace, err := overlay.ResolveWithTrace(context.Background(), "PaymentAuth", store, 100)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if env.Outcome != OutcomeBound {
		t.Errorf("outcome = %s, want %s", env.Outcome, OutcomeBound)
	}
	if len(env.Matches) != 1 || env.Matches[0].EntityID != "f1" {
		t.Errorf("matches = %+v", env.Matches)
	}
	if len(trace) != 1 || trace[0].Outcome != string(OutcomeBound) {
		t.Errorf("trace = %+v", trace)
	}
}

func TestResolve_FallsThroughToBodyHashAfterRename(t *testing.T) {
	store := openTestStore(t)
	// Rename scenario: the original qualified_name is gone; an entity with
	// a different qualified_name carries the same body_hash.
	putFn(t, store, code_core.Entity{
		ID: "f2", Kind: code_core.KindFunction, LanguageID: "go",
		QualifiedName: "payments.PaymentService.authorizeV2",
		BodyHash:      "sha256:abcd",
	})
	sel := parseSelectorOrFail(t, `selector PaymentAuth {
		unique
		anchor qualified_name "payments.PaymentService.authorize"
		anchor body_hash "sha256:abcd"
	}`)
	overlay := newOverlayWithSelector(sel)
	env, trace, err := overlay.ResolveWithTrace(context.Background(), "PaymentAuth", store, 100)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if env.Outcome != OutcomeReanchored {
		t.Errorf("outcome = %s, want %s", env.Outcome, OutcomeReanchored)
	}
	if len(env.Matches) != 1 || env.Matches[0].EntityID != "f2" {
		t.Errorf("matches = %+v", env.Matches)
	}
	if env.Matches[0].ViaAnchor != "body_hash" {
		t.Errorf("via_anchor = %s, want body_hash", env.Matches[0].ViaAnchor)
	}
	// Trace should record the qualified_name miss + the body_hash hit.
	if trace[0].Skipped != true {
		t.Errorf("primary should be skipped (no candidates), got %+v", trace[0])
	}
	if trace[1].Outcome != string(OutcomeReanchored) {
		t.Errorf("fallback trace = %+v", trace[1])
	}
}

func TestResolve_NoMatchesReturnsUnresolved(t *testing.T) {
	store := openTestStore(t)
	sel := parseSelectorOrFail(t, `selector Missing {
		anchor qualified_name "does.not.exist"
		anchor body_hash "sha256:nope"
	}`)
	env, trace, err := newOverlayWithSelector(sel).ResolveWithTrace(context.Background(), "Missing", store, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if env.Outcome != OutcomeUnresolved {
		t.Errorf("outcome = %s, want %s", env.Outcome, OutcomeUnresolved)
	}
	if len(trace) != 2 {
		t.Errorf("trace len = %d, want 2", len(trace))
	}
}

func TestResolve_UniqueCardinalityViolationFallsToUnresolved(t *testing.T) {
	store := openTestStore(t)
	// Two entities share the same body_hash → unique selector can't pick.
	putFn(t, store, code_core.Entity{
		ID: "a", Kind: code_core.KindFunction, LanguageID: "go",
		QualifiedName: "pkg.A", BodyHash: "sha256:dup",
	})
	putFn(t, store, code_core.Entity{
		ID: "b", Kind: code_core.KindFunction, LanguageID: "go",
		QualifiedName: "pkg.B", BodyHash: "sha256:dup",
	})
	sel := parseSelectorOrFail(t, `selector Dup {
		unique
		anchor qualified_name "missing.X"
		anchor body_hash "sha256:dup"
	}`)
	env, trace, err := newOverlayWithSelector(sel).ResolveWithTrace(context.Background(), "Dup", store, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if env.Outcome != OutcomeUnresolved {
		t.Errorf("outcome = %s, want unresolved (cardinality)", env.Outcome)
	}
	// The body_hash trace entry should record the cardinality violation.
	bhEntry := trace[1]
	if bhEntry.Outcome != string(OutcomeUnresolved) {
		t.Errorf("expected unresolved on cardinality, got %+v", bhEntry)
	}
	if !bhEntry.CardinalityViolation() {
		t.Errorf("CardinalityViolation should be true, trace=%+v", bhEntry)
	}
}

func TestResolve_PathGlobAlone_BelowThreshold_StaysUnresolved(t *testing.T) {
	store := openTestStore(t)
	putFn(t, store, code_core.Entity{
		ID: "c", Kind: code_core.KindFunction, LanguageID: "go",
		QualifiedName: "pkg.Foo", Path: "src/pkg/foo.go",
	})
	// path_glob alone scores 0.55, below default reanchored 0.75 → unresolved.
	sel := parseSelectorOrFail(t, `selector PathOnly {
		fallback path_glob "src/**/*.go"
	}`)
	env, _, _ := newOverlayWithSelector(sel).ResolveWithTrace(context.Background(), "PathOnly", store, 1)
	if env.Outcome != OutcomeUnresolved {
		t.Errorf("outcome = %s, want unresolved (path_glob below default reanchored threshold)", env.Outcome)
	}
}

func TestResolve_FunctionSignatureMatchesAfterRename(t *testing.T) {
	store := openTestStore(t)
	// NormalizedSignature uses the entity's native Go shape; the anchor
	// uses the SPEC §11.2 surface form. The evaluator bridges via shape-
	// equivalent comparison (params + return type, names stripped).
	putFn(t, store, code_core.Entity{
		ID: "fs1", Kind: code_core.KindFunction, LanguageID: "go",
		QualifiedName:       "pkg.AuthorizeRenamed",
		NormalizedSignature: "(amount Money, card Card) AuthResult",
	})
	sel := parseSelectorOrFail(t, `selector Auth {
		anchor qualified_name "pkg.Authorize"
		anchor function_signature sig(Money, Card) -> AuthResult
	}`)
	env, _, err := newOverlayWithSelector(sel).ResolveWithTrace(context.Background(), "Auth", store, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if env.Outcome != OutcomeReanchored {
		t.Errorf("outcome = %s, want reanchored via function_signature", env.Outcome)
	}
	if len(env.Matches) != 1 || env.Matches[0].EntityID != "fs1" {
		t.Errorf("matches = %+v", env.Matches)
	}
}

func TestResolve_ThresholdOverrideBoundsLowerScores(t *testing.T) {
	store := openTestStore(t)
	putFn(t, store, code_core.Entity{
		ID: "p1", Kind: code_core.KindFunction, LanguageID: "go",
		QualifiedName:       "pkg.X",
		NormalizedSignature: "() int",
	})
	// Author lowers `bound` to 0.80 — function_signature (0.85) clears it,
	// so even when used as the *primary* anchor it returns bound.
	sel := parseSelectorOrFail(t, `selector LowerBound {
		anchor function_signature sig() -> int
		thresholds {
			bound: 0.80
			reanchored: 0.5
		}
	}`)
	env, _, _ := newOverlayWithSelector(sel).ResolveWithTrace(context.Background(), "LowerBound", store, 1)
	if env.Outcome != OutcomeBound {
		t.Errorf("outcome = %s, want bound (custom thresholds)", env.Outcome)
	}
}

// TestResolve_ExplainTrace_ShowsFallbackWinAndSkippedRest exercises plan §3
// gate criterion 8 at the per-anchor level: when the primary anchor misses,
// the trace records the miss, the fallback's win, and marks lower-priority
// anchors as not consulted.
func TestResolve_ExplainTrace_ShowsFallbackWinAndSkippedRest(t *testing.T) {
	store := openTestStore(t)
	// The renamed function exists with the original body_hash. The selector's
	// qualified_name points at the stale name; body_hash is the fingerprint
	// fallback; ast_hash is below body_hash on the ladder and must NOT be
	// consulted because body_hash already won.
	putFn(t, store, code_core.Entity{
		ID: "renamed", Kind: code_core.KindFunction, LanguageID: "go",
		QualifiedName: "checkout.CheckoutValidator.ValidateV2",
		BodyHash:      "sha256:body-of-validate",
		ASTHash:       "ast:original",
	})
	sel := parseSelectorOrFail(t, `selector CheckoutValidator {
		anchor qualified_name "checkout.CheckoutValidator.Validate"
		anchor body_hash "sha256:body-of-validate"
		anchor ast_hash "ast:original"
	}`)
	env, trace, err := newOverlayWithSelector(sel).ResolveWithTrace(context.Background(), "CheckoutValidator", store, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if env.Outcome != OutcomeReanchored {
		t.Errorf("outcome = %s, want reanchored", env.Outcome)
	}
	if len(env.Matches) != 1 || env.Matches[0].ViaAnchor != "body_hash" {
		t.Errorf("matches = %+v; want body_hash via_anchor", env.Matches)
	}
	if len(trace) != 3 {
		t.Fatalf("trace len = %d, want 3; trace=%+v", len(trace), trace)
	}
	if trace[0].Kind != "qualified_name" || !trace[0].Skipped ||
		!strings.Contains(trace[0].Reason, "no candidates") {
		t.Errorf("primary trace not as expected: %+v", trace[0])
	}
	if trace[1].Kind != "body_hash" || trace[1].Outcome != string(OutcomeReanchored) ||
		!strings.Contains(trace[1].Reason, "fallback scored") {
		t.Errorf("fallback trace not as expected: %+v", trace[1])
	}
	if trace[2].Kind != "ast_hash" || !trace[2].Skipped ||
		!strings.Contains(trace[2].Reason, "not consulted") {
		t.Errorf("skipped-remaining trace not as expected: %+v", trace[2])
	}
}

// TestResolve_FullReanchorScenario exercises plan §3 gate criterion 6 at the
// resolver level: a selector with qualified_name + function_signature +
// body_hash anchors. Setup mirrors a TS function rename — the original
// qualified_name is gone, the renamed function carries an unchanged
// signature + body_hash, and resolution must surface as `reanchored` with
// confidence ≥ 0.75 via the fingerprint fallback.
func TestResolve_FullReanchorScenario(t *testing.T) {
	store := openTestStore(t)
	putFn(t, store, code_core.Entity{
		ID: "ts1", Kind: code_core.KindMethod, LanguageID: "typescript",
		QualifiedName:       "CheckoutValidator.preValidate", // renamed
		NormalizedSignature: "(cart: Cart) => void",
		BodyHash:            "sha256:body-validate",
	})
	sel := parseSelectorOrFail(t, `selector CheckoutValidator {
		anchor qualified_name "CheckoutValidator.validate"
		anchor function_signature sig(Cart) -> void
		anchor body_hash "sha256:body-validate"
	}`)
	env, _, err := newOverlayWithSelector(sel).ResolveWithTrace(context.Background(), "CheckoutValidator", store, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if env.Outcome != OutcomeReanchored {
		t.Errorf("outcome = %s, want reanchored (rename → fingerprint fallback)", env.Outcome)
	}
	if len(env.Matches) == 0 {
		t.Fatal("expected ≥1 match")
	}
	if env.Matches[0].Confidence < 0.75 {
		t.Errorf("match confidence %.3f < 0.75 (gate criterion 6)", env.Matches[0].Confidence)
	}
	if env.Matches[0].EntityID != "ts1" {
		t.Errorf("matched entity %s, want ts1", env.Matches[0].EntityID)
	}
}

// TestResolve_LanguageIDFiltersPolyglotMatches asserts that a selector
// with `anchor language_id "ts"` filters every other anchor's match list
// to TypeScript-tagged entities only, even when qualified_name matches
// across all three languages. Plan §7 explicit gate criterion.
func TestResolve_LanguageIDFiltersPolyglotMatches(t *testing.T) {
	store := openTestStore(t)
	for _, e := range []code_core.Entity{
		{ID: "go1", Kind: code_core.KindMethod, LanguageID: "go", QualifiedName: "CheckoutValidator.validate"},
		{ID: "ts1", Kind: code_core.KindMethod, LanguageID: "typescript", QualifiedName: "CheckoutValidator.validate"},
		{ID: "py1", Kind: code_core.KindMethod, LanguageID: "python", QualifiedName: "CheckoutValidator.validate"},
	} {
		putFn(t, store, e)
	}
	sel := parseSelectorOrFail(t, `selector CheckoutValidatorTS {
		anchor qualified_name "CheckoutValidator.validate"
		anchor language_id "ts"
	}`)
	env, trace, err := newOverlayWithSelector(sel).ResolveWithTrace(context.Background(), "CheckoutValidatorTS", store, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if env.Outcome != OutcomeBound {
		t.Errorf("outcome = %s, want bound", env.Outcome)
	}
	if len(env.Matches) != 1 {
		t.Fatalf("matches = %d, want 1; envelope=%+v", len(env.Matches), env.Matches)
	}
	if env.Matches[0].EntityID != "ts1" {
		t.Errorf("matched %s, want ts1", env.Matches[0].EntityID)
	}
	// Trace should record the language_id filter as a dedicated rung.
	sawFilter := false
	for _, t1 := range trace {
		if t1.Kind == "language_id" {
			sawFilter = true
			if !strings.Contains(t1.Reason, "filter active") {
				t.Errorf("filter rung reason = %q, want 'filter active'", t1.Reason)
			}
		}
	}
	if !sawFilter {
		t.Errorf("expected language_id filter rung in trace; got %+v", trace)
	}
}

func TestResolve_LanguageIDExcludesEverythingWhenNoMatch(t *testing.T) {
	store := openTestStore(t)
	putFn(t, store, code_core.Entity{
		ID: "go1", Kind: code_core.KindMethod, LanguageID: "go",
		QualifiedName: "CheckoutValidator.validate",
	})
	sel := parseSelectorOrFail(t, `selector OnlyTS {
		anchor qualified_name "CheckoutValidator.validate"
		anchor language_id "ts"
	}`)
	env, _, _ := newOverlayWithSelector(sel).ResolveWithTrace(context.Background(), "OnlyTS", store, 1)
	if env.Outcome != OutcomeUnresolved {
		t.Errorf("outcome = %s, want unresolved (no TS entity exists)", env.Outcome)
	}
}

func TestResolve_CacheReusesEnvelopeAtSameSeq(t *testing.T) {
	store := openTestStore(t)
	putFn(t, store, code_core.Entity{
		ID: "z", Kind: code_core.KindFunction, LanguageID: "go",
		QualifiedName: "pkg.Z",
	})
	sel := parseSelectorOrFail(t, `selector Z {
		anchor qualified_name "pkg.Z"
	}`)
	r, err := NewResolver(newOverlayWithSelector(sel), store, nil)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	a, _, _ := r.Resolve(context.Background(), "Z", 42)
	b, _, _ := r.Resolve(context.Background(), "Z", 42)
	if a != b {
		t.Errorf("expected same envelope pointer on cache hit; got %p vs %p", a, b)
	}
}
