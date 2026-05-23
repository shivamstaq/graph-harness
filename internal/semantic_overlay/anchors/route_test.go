package anchors

import (
	"context"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/code_core"
)

func TestRoutePattern_GlobMatchesRouteEntities(t *testing.T) {
	store := newTestStore(t)
	putEntity(t, store, code_core.Entity{
		ID: "r1", Kind: "Route", QualifiedName: "/v1/orders/:id", KindTag: "GET", LanguageID: "go",
	})
	putEntity(t, store, code_core.Entity{
		ID: "r2", Kind: "Route", QualifiedName: "/v1/orders", KindTag: "POST", LanguageID: "go",
	})
	putEntity(t, store, code_core.Entity{
		ID: "r3", Kind: "Route", QualifiedName: "/healthz", KindTag: "GET", LanguageID: "go",
	})
	// Non-Route entity must be ignored.
	putEntity(t, store, code_core.Entity{
		ID: "fn1", Kind: code_core.KindFunction, QualifiedName: "/v1/orders/handler", LanguageID: "go",
	})

	matches, err := RoutePattern{}.Evaluate(context.Background(), strAnchor("route_pattern", "/v1/orders/**"), store)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	gotIDs := map[string]bool{}
	for _, m := range matches {
		gotIDs[m.EntityID] = true
		if m.Confidence != ConfidenceRoutePattern {
			t.Errorf("confidence = %v, want %v", m.Confidence, ConfidenceRoutePattern)
		}
	}
	if !gotIDs["r1"] || !gotIDs["r2"] {
		t.Errorf("expected r1 and r2 to match; got %v", gotIDs)
	}
	if gotIDs["r3"] {
		t.Errorf("/healthz should not match /v1/orders/**")
	}
	if gotIDs["fn1"] {
		t.Errorf("non-Route entity should not match")
	}
}

func TestRouteMethod_ExactMatchOnKindTag(t *testing.T) {
	store := newTestStore(t)
	putEntity(t, store, code_core.Entity{
		ID: "r1", Kind: "Route", QualifiedName: "/v1/orders", KindTag: "GET",
	})
	putEntity(t, store, code_core.Entity{
		ID: "r2", Kind: "Route", QualifiedName: "/v1/orders", KindTag: "POST",
	})
	matches, err := RouteMethod{}.Evaluate(context.Background(), strAnchor("route_method", "GET"), store)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(matches) != 1 || matches[0].EntityID != "r1" {
		t.Fatalf("got %+v, want exactly r1", matches)
	}
	if matches[0].Confidence != ConfidenceRouteMethod {
		t.Errorf("confidence = %v, want %v", matches[0].Confidence, ConfidenceRouteMethod)
	}
}

func TestRoutePattern_RegisteredAtCanonicalKindString(t *testing.T) {
	reg := Registry()
	if _, ok := reg["route_pattern"].(RoutePattern); !ok {
		t.Errorf("Registry['route_pattern'] missing/wrong type")
	}
	if _, ok := reg["route_method"].(RouteMethod); !ok {
		t.Errorf("Registry['route_method'] missing/wrong type")
	}
}
