package anchors

import (
	"context"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/code_core"
)

func TestSchemaField_MatchesAcrossTables(t *testing.T) {
	store := newTestStore(t)
	putEntity(t, store, code_core.Entity{
		ID: "f1", Kind: "SchemaField", QualifiedName: "users.email",
	})
	putEntity(t, store, code_core.Entity{
		ID: "f2", Kind: "SchemaField", QualifiedName: "accounts.email",
	})
	putEntity(t, store, code_core.Entity{
		ID: "f3", Kind: "SchemaField", QualifiedName: "users.name",
	})
	// Schema (table) entity — must be ignored by schema_field.
	putEntity(t, store, code_core.Entity{
		ID: "s1", Kind: "Schema", QualifiedName: "users",
	})

	matches, err := SchemaField{}.Evaluate(context.Background(), strAnchor("schema_field", "email"), store)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	gotIDs := map[string]bool{}
	for _, m := range matches {
		gotIDs[m.EntityID] = true
		if m.Confidence != ConfidenceSchemaField {
			t.Errorf("confidence = %v, want %v", m.Confidence, ConfidenceSchemaField)
		}
	}
	if !gotIDs["f1"] || !gotIDs["f2"] {
		t.Errorf("expected f1 and f2 to match; got %v", gotIDs)
	}
	if gotIDs["f3"] || gotIDs["s1"] {
		t.Errorf("unexpected match: %v", gotIDs)
	}
}

func TestSchemaTable_MatchesSchemaEntities(t *testing.T) {
	store := newTestStore(t)
	putEntity(t, store, code_core.Entity{
		ID: "s1", Kind: "Schema", QualifiedName: "users",
	})
	putEntity(t, store, code_core.Entity{
		ID: "s2", Kind: "Schema", QualifiedName: "accounts",
	})
	// SchemaField with matching trailing segment — must be ignored.
	putEntity(t, store, code_core.Entity{
		ID: "f1", Kind: "SchemaField", QualifiedName: "users.id",
	})

	matches, err := SchemaTable{}.Evaluate(context.Background(), strAnchor("schema_table", "users"), store)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(matches) != 1 || matches[0].EntityID != "s1" {
		t.Fatalf("got %+v, want exactly s1", matches)
	}
	if matches[0].Confidence != ConfidenceSchemaTable {
		t.Errorf("confidence = %v, want %v", matches[0].Confidence, ConfidenceSchemaTable)
	}
}

func TestSchema_RegisteredAtCanonicalKindStrings(t *testing.T) {
	reg := Registry()
	if _, ok := reg["schema_field"].(SchemaField); !ok {
		t.Errorf("Registry['schema_field'] missing/wrong type")
	}
	if _, ok := reg["schema_table"].(SchemaTable); !ok {
		t.Errorf("Registry['schema_table'] missing/wrong type")
	}
}
