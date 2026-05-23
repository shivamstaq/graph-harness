package anchors

import (
	"context"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/code_core"
)

func TestKindwise_MatchesExactKindAtFullConfidence(t *testing.T) {
	store := newTestStore(t)
	putEntity(t, store, code_core.Entity{
		ID: "route-1", Kind: "Route", LanguageID: "go",
		QualifiedName: "GET /v1/orders",
		Path:          "routes/orders.go",
	})
	putEntity(t, store, code_core.Entity{
		ID: "fn-1", Kind: code_core.KindFunction, LanguageID: "go",
		QualifiedName: "orders.Handler",
		Path:          "routes/orders.go",
	})

	matches, err := Kindwise{}.Evaluate(context.Background(), strAnchor("entity_kind", "Route"), store)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("got %d matches, want 1: %+v", len(matches), matches)
	}
	if matches[0].EntityID != "route-1" {
		t.Errorf("matched entity = %q, want %q", matches[0].EntityID, "route-1")
	}
	if matches[0].Confidence != ConfidenceEntityKind {
		t.Errorf("confidence = %v, want %v", matches[0].Confidence, ConfidenceEntityKind)
	}
}

func TestKindwise_EmptyValueReturnsNoMatches(t *testing.T) {
	store := newTestStore(t)
	putEntity(t, store, code_core.Entity{
		ID: "fn-1", Kind: code_core.KindFunction, QualifiedName: "x.y", LanguageID: "go",
	})
	matches, err := Kindwise{}.Evaluate(context.Background(), strAnchor("entity_kind", ""), store)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected no matches for empty value; got %+v", matches)
	}
}

func TestKindwise_RegisteredAtCanonicalKindString(t *testing.T) {
	reg := Registry()
	ev, ok := reg["entity_kind"]
	if !ok {
		t.Fatalf("Registry missing 'entity_kind' evaluator")
	}
	if _, ok := ev.(Kindwise); !ok {
		t.Errorf("Registry['entity_kind'] = %T, want Kindwise", ev)
	}
}
