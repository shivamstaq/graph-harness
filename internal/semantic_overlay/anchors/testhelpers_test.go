package anchors

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/dsl"
)

// newTestStore opens a fresh in-memory code.core store for evaluator
// tests. Each call returns an isolated database so subtests can seed
// disjoint entity sets without cross-talk.
func newTestStore(t *testing.T) *code_core.Store {
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

// putEntity is a one-line PutEntity wrapper to keep test fixtures terse.
func putEntity(t *testing.T, store *code_core.Store, e code_core.Entity) {
	t.Helper()
	if err := store.PutEntity(context.Background(), e, 1); err != nil {
		t.Fatalf("PutEntity %s: %v", e.ID, err)
	}
}

// strAnchor builds a string-valued Anchor for evaluator tests. Mirrors
// the shape the dsl parser produces for `anchor <kind> "<value>"`.
func strAnchor(kind, value string) *dsl.Anchor {
	v := value
	return &dsl.Anchor{
		Marker: "anchor",
		Kind:   kind,
		Value:  &dsl.AnchorValue{Str: &v},
	}
}
