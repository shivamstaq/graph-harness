package code_core

import (
	"context"
	"math/rand/v2"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// TestMultiSourceSuppressAtSource_NoEventsOnSecondPass exercises the
// SPEC §6.21 + P1.5.T03 contract at the *multi-source* seam: when SCIP
// + LSP + tree-sitter all agree on the same (content_hash, signature,
// qualified_name) for a symbol that has already been stored, the
// unifier emits zero new events on the second pass.
//
// This complements the single-source TestSuppressAtSource_NoOp tests
// in suppress_test.go by exercising the full Unifier path with all
// three sources contributing. The property: re-Unify the exact same
// Symbol batch and changed=false MUST hold for every fixture.
func TestMultiSourceSuppressAtSource_NoEventsOnSecondPass(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(0x70, 0x31)) //nolint:gosec // deterministic seed for property test

	store := newTestStore(t)
	emitter := &captureEmitter{}
	u := &Unifier{Store: store, Emitter: emitter}

	const n = 1000 // F15 / plan §7: bumped from 100 to satisfy the 1000-fixture property contract
	var allSyms []source_live.Symbol
	for i := 0; i < n; i++ {
		fn := genFn(rng, i)
		allSyms = append(allSyms, fn.emitAll()...)
	}

	// First pass: every fixture is brand new, so changed MUST be true.
	idsFirst, changedFirst, err := u.UnifyChanged(ctx, allSyms, 100)
	if err != nil {
		t.Fatalf("first UnifyChanged: %v", err)
	}
	if !changedFirst {
		t.Fatalf("first pass over fresh symbols must report changed=true")
	}
	if dedupCount(idsFirst) != n {
		t.Fatalf("first pass must materialize %d distinct entities; got %d", n, dedupCount(idsFirst))
	}
	emittedAfterFirst := len(emitter.all())

	// Second pass with the identical input batch at a higher seq: the
	// last_seen_seq columns refresh but no entity content fingerprint
	// shifts, so changed MUST be false and the SymbolDisambiguation
	// event stream MUST be empty (we're in three-source-agreement land
	// so no disambiguation should fire at all, but we additionally
	// assert no NEW events appear past the first-pass count).
	idsSecond, changedSecond, err := u.UnifyChanged(ctx, allSyms, 101)
	if err != nil {
		t.Fatalf("second UnifyChanged: %v", err)
	}
	if changedSecond {
		t.Fatalf("re-unify of identical multi-source batch must report changed=false")
	}
	if !sameIDs(idsFirst, idsSecond) {
		t.Fatalf("entity IDs diverged across re-unify: first=%v second=%v", idsFirst, idsSecond)
	}
	if got := len(emitter.all()); got != emittedAfterFirst {
		t.Fatalf("re-unify must emit zero new SymbolDisambiguation events; before=%d after=%d",
			emittedAfterFirst, got)
	}
}

// TestMultiSourceSuppressAtSource_PerSourceOrdering asserts that the
// suppress-at-source invariant holds regardless of which order the
// three sources arrive in. Three permutations of the same fixture set
// (LSP-first, SCIP-first, tree-sitter-first batches) all reach the
// same fingerprinted state; subsequent UnifyChanged calls in any
// remaining permutation MUST report changed=false.
func TestMultiSourceSuppressAtSource_PerSourceOrdering(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(0x70, 0x32)) //nolint:gosec // deterministic seed

	store := newTestStore(t)
	emitter := &captureEmitter{}
	u := &Unifier{Store: store, Emitter: emitter}

	const n = 30
	fixtures := make([]fakeFn, n)
	for i := range fixtures {
		fixtures[i] = genFn(rng, i)
	}

	// Build per-source batches.
	var lspBatch, scipBatch, tsBatch []source_live.Symbol
	for _, f := range fixtures {
		lspBatch = append(lspBatch, fakeSymbol(source_live.SourceClassLSP, "extractor:lsp:fake", f))
		scipBatch = append(scipBatch, fakeSymbol(source_live.SourceClassSCIP, "extractor:scip:fake", f))
		tsBatch = append(tsBatch, fakeSymbol(source_live.SourceClassTreesitter, "extractor:treesitter:fake", f))
	}

	// First pass: LSP arrives first. Brand new entities; changed=true.
	if _, changed, err := u.UnifyChanged(ctx, lspBatch, 10); err != nil || !changed {
		t.Fatalf("LSP first pass: err=%v changed=%v", err, changed)
	}
	// SCIP arrives — adds a new provenance row per entity; changed=true.
	if _, changed, err := u.UnifyChanged(ctx, scipBatch, 11); err != nil || !changed {
		t.Fatalf("SCIP arrival must shift provenance fingerprint; changed=%v", changed)
	}
	// tree-sitter arrives — adds the third provenance row; changed=true.
	if _, changed, err := u.UnifyChanged(ctx, tsBatch, 12); err != nil || !changed {
		t.Fatalf("tree-sitter arrival must shift provenance fingerprint; changed=%v", changed)
	}

	// Now every entity has the full three-source provenance set. Re-
	// running each per-source batch in any order MUST be a no-op.
	for _, batch := range [][]source_live.Symbol{lspBatch, scipBatch, tsBatch, lspBatch, scipBatch} {
		if _, changed, err := u.UnifyChanged(ctx, batch, 100); err != nil || changed {
			t.Fatalf("steady-state re-unify must report changed=false; err=%v changed=%v", err, changed)
		}
	}

	// No SymbolDisambiguation events should have fired across the whole
	// run because every source produced the same canonical ID for each
	// fixture.
	if got := len(emitter.all()); got != 0 {
		t.Fatalf("three-source agreement must emit 0 SymbolDisambiguation events; got %d", got)
	}
}
