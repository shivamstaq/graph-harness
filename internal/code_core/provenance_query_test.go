package code_core

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// TestLookupEntity_Shape demonstrates the EntityView contract on a
// three-source-agreement fixture. T-surfaces-bench's drill-down (and
// the MCP `gh://entity/code.core/<kind>/<id>` resource) should
// marshal an EntityView straight out of [Store.LookupEntity] — this
// test is the canonical example.
func TestLookupEntity_Shape(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	u := &Unifier{Store: store}
	ctx := context.Background()
	f := genFn(rand.New(rand.NewPCG(11, 11)), 3) //nolint:gosec
	if _, err := u.Unify(ctx, f.emitAll(), 42); err != nil {
		t.Fatalf("Unify: %v", err)
	}
	ent, ok := entityFromSymbol(fakeSymbol(source_live.SourceClassLSP, "x", f))
	if !ok {
		t.Fatalf("entityFromSymbol: not ok")
	}

	view, err := store.LookupEntity(ctx, ent.ID)
	if err != nil {
		t.Fatalf("LookupEntity: %v", err)
	}

	// Entity body round-trips through the persistence layer.
	if view.Entity.ID != ent.ID {
		t.Errorf("Entity.ID = %q; want %q", view.Entity.ID, ent.ID)
	}
	if view.Entity.QualifiedName != f.qn {
		t.Errorf("Entity.QualifiedName = %q; want %q", view.Entity.QualifiedName, f.qn)
	}
	if view.Entity.NormalizedSignature == "" {
		t.Errorf("Entity.NormalizedSignature should be persisted")
	}

	// Provenance carries one entry per source, in canonical order
	// (index_scip < live_lsp < structural_treesitter alphabetically).
	if got := view.Provenance.Summary.SourceCount; got != 3 {
		t.Errorf("Summary.SourceCount = %d; want 3", got)
	}
	wantOrder := []SourceClass{SourceClassSCIP, SourceClassLSP, SourceClassTreesitter}
	for i, want := range wantOrder {
		if got := view.Provenance.Sources[i].SourceClass; got != want {
			t.Errorf("Sources[%d].SourceClass = %q; want %q", i, got, want)
		}
	}

	// Folded summary is min-confidence + worst-freshness across sources.
	// genFn assigns 0.9..1.0 confidences; folded confidence must be the
	// lowest of the three.
	low := view.Provenance.Sources[0].Confidence
	for _, e := range view.Provenance.Sources[1:] {
		if e.Confidence < low {
			low = e.Confidence
		}
	}
	if view.Provenance.Summary.Confidence != low {
		t.Errorf("Summary.Confidence = %v; want %v (min over sources)", view.Provenance.Summary.Confidence, low)
	}

	// The view must marshal to JSON cleanly so the MCP adapter can
	// stream it as the resource body without an intermediate DTO.
	if _, err := json.Marshal(view); err != nil {
		t.Fatalf("json.Marshal(EntityView): %v", err)
	}
}

func TestLookupEntity_NotFound(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	_, err := store.LookupEntity(context.Background(), "no-such-id")
	if !errors.Is(err, ErrEntityNotFound) {
		t.Errorf("LookupEntity on missing id should return ErrEntityNotFound; got %v", err)
	}
}

func TestLookupProvenance_NotFound(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	_, err := store.LookupProvenance(context.Background(), "no-such-id")
	if !errors.Is(err, ErrEntityNotFound) {
		t.Errorf("LookupProvenance on missing id should return ErrEntityNotFound; got %v", err)
	}
}

// TestFoldProvenance_WorstFreshnessWins pins the §4.5 rule that the
// folded freshness is the most-pessimistic per-source value: a single
// `dirty` source poisons a `live` + `current` consensus.
func TestFoldProvenance_WorstFreshnessWins(t *testing.T) {
	t.Parallel()
	got := foldProvenance([]SourceEntry{
		{SourceClass: SourceClassLSP, Confidence: 0.95, Freshness: FreshnessLive},
		{SourceClass: SourceClassSCIP, Confidence: 0.99, Freshness: FreshnessDirty},
		{SourceClass: SourceClassTreesitter, Confidence: 0.90, Freshness: FreshnessCurrent},
	})
	if got.Freshness != FreshnessDirty {
		t.Errorf("Summary.Freshness = %q; want %q (worst-case fold)", got.Freshness, FreshnessDirty)
	}
	if got.Confidence != 0.90 {
		t.Errorf("Summary.Confidence = %v; want 0.90 (min fold)", got.Confidence)
	}
	if got.SourceCount != 3 {
		t.Errorf("Summary.SourceCount = %d; want 3", got.SourceCount)
	}
}

func TestFoldProvenance_LatestSeenSeq(t *testing.T) {
	t.Parallel()
	got := foldProvenance([]SourceEntry{
		{SourceClass: SourceClassLSP, LastSeenSeq: 10},
		{SourceClass: SourceClassSCIP, LastSeenSeq: 42},
		{SourceClass: SourceClassTreesitter, LastSeenSeq: 7},
	})
	if got.LatestSeenSeq != 42 {
		t.Errorf("Summary.LatestSeenSeq = %d; want 42 (max fold)", got.LatestSeenSeq)
	}
}

func TestFoldProvenance_Empty(t *testing.T) {
	t.Parallel()
	got := foldProvenance(nil)
	if got.SourceCount != 0 {
		t.Errorf("empty fold should yield zero summary; got %+v", got)
	}
}
