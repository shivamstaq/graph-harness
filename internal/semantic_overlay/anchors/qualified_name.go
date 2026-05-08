package anchors

import (
	"context"
	"fmt"

	"github.com/shivamstaq/graph-harness/internal/dsl"
)

// QualifiedName is the nominal anchor: exact string equality on the
// `qualified_name` attribute (SPEC §3.1, §6.12).
type QualifiedName struct{}

// Kind returns "qualified_name".
func (QualifiedName) Kind() string { return "qualified_name" }

// Evaluate looks up a single entity by qualified name and reports it at
// [ConfidenceQualifiedName]. Missing entity → no matches.
func (QualifiedName) Evaluate(ctx context.Context, a *dsl.Anchor, store Lookup) ([]Match, error) {
	qn := stringValue(a)
	if qn == "" {
		return nil, nil
	}
	ent, err := store.LookupByQualifiedName(ctx, qn)
	if err != nil {
		return nil, err
	}
	if ent == nil {
		return nil, nil
	}
	return []Match{matchFromEntity(*ent, ConfidenceQualifiedName,
		fmt.Sprintf("qualified_name == %q", qn))}, nil
}
