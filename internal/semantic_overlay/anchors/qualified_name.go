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

// Evaluate looks up every entity whose qualified_name matches and reports
// each at [ConfidenceQualifiedName]. Returning the full candidate set
// (rather than just the first row) lets `unique` selectors raise
// cardinality violations and lets a `language_id` filter narrow polyglot
// matches to one language. Missing entities → no matches.
func (QualifiedName) Evaluate(ctx context.Context, a *dsl.Anchor, store Lookup) ([]Match, error) {
	qn := stringValue(a)
	if qn == "" {
		return nil, nil
	}
	ents, err := store.LookupAllByQualifiedName(ctx, qn)
	if err != nil {
		return nil, err
	}
	out := make([]Match, 0, len(ents))
	for _, e := range ents {
		out = append(out, matchFromEntity(e, ConfidenceQualifiedName,
			fmt.Sprintf("qualified_name == %q", qn)))
	}
	return out, nil
}
