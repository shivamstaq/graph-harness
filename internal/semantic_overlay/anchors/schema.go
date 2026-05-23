package anchors

import (
	"context"
	"fmt"
	"sort"

	"github.com/shivamstaq/graph-harness/internal/dsl"
)

// Schema-family anchor evaluators target code.framework Schema and
// SchemaField entities. Pass 1 extractors materialize them with:
//
//	Schema       Kind="Schema"       QualifiedName=table_name
//	SchemaField  Kind="SchemaField"  QualifiedName=table_name + "." + field_name
//
// The schema_field anchor matches the field portion (the last
// dot-separated segment of QualifiedName) so authors can write
// `anchor schema_field "email"` without spelling out every table that
// hosts an `email` column. schema_table matches the whole
// QualifiedName for Schema entities (exact eq).

// SchemaField matches SchemaField entities whose column name equals
// the anchor's value (case-sensitive). The match is across every
// table that defines a column by that name, which is the desired
// semantics for cross-cutting invariants like "no PII columns".
type SchemaField struct{}

// Kind returns "schema_field".
func (SchemaField) Kind() string { return "schema_field" }

// ConfidenceSchemaField is the score SchemaField assigns to an exact
// match. Field names are often shared across tables (e.g. `id`,
// `created_at`), so confidence reflects "narrow but not unique."
const ConfidenceSchemaField = 0.75

// Evaluate filters SchemaField entities by exact match on the trailing
// segment of QualifiedName.
func (SchemaField) Evaluate(ctx context.Context, a *dsl.Anchor, store Lookup) ([]Match, error) {
	want := stringValue(a)
	if want == "" {
		return nil, nil
	}
	ents, err := store.LookupAllEntities(ctx)
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, e := range ents {
		if string(e.Kind) != "SchemaField" {
			continue
		}
		if lastDotSegment(e.QualifiedName) != want {
			continue
		}
		out = append(out, matchFromEntity(e, ConfidenceSchemaField,
			fmt.Sprintf("schema_field == %q", want)))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].QualifiedName < out[j].QualifiedName
	})
	return out, nil
}

// SchemaTable matches Schema entities whose table name equals the
// anchor's value. Table names are workspace-unique by convention so
// this anchor is the schema-side counterpart to qualified_name.
type SchemaTable struct{}

// Kind returns "schema_table".
func (SchemaTable) Kind() string { return "schema_table" }

// ConfidenceSchemaTable is the score SchemaTable assigns to an exact
// match. Table-name equality is high-precision; the score sits just
// below qualified_name to leave room for the nominal anchor to win
// when both are present on a selector.
const ConfidenceSchemaTable = 0.92

// Evaluate filters Schema entities by exact QualifiedName equality.
func (SchemaTable) Evaluate(ctx context.Context, a *dsl.Anchor, store Lookup) ([]Match, error) {
	want := stringValue(a)
	if want == "" {
		return nil, nil
	}
	ents, err := store.LookupAllEntities(ctx)
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, e := range ents {
		if string(e.Kind) != "Schema" {
			continue
		}
		if e.QualifiedName != want {
			continue
		}
		out = append(out, matchFromEntity(e, ConfidenceSchemaTable,
			fmt.Sprintf("schema_table == %q", want)))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].QualifiedName < out[j].QualifiedName
	})
	return out, nil
}

// lastDotSegment returns the substring after the final '.', or the
// whole string if no '.' is present. Used to peel the field name off
// SchemaField qualified names of the form "<table>.<field>".
func lastDotSegment(qn string) string {
	for i := len(qn) - 1; i >= 0; i-- {
		if qn[i] == '.' {
			return qn[i+1:]
		}
	}
	return qn
}
