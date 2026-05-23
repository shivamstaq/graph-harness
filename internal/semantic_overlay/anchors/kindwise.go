package anchors

import (
	"context"
	"fmt"
	"sort"

	"github.com/shivamstaq/graph-harness/internal/dsl"
)

// Kindwise is the entity-kind selector anchor: exact-string equality on
// the candidate entity's `Kind` field. The anchor's value is the
// EntityKind literal as it appears in the layer manifests — e.g.
// "Class", "Interface", "Function", "Route", "EventPublisher", or any
// future framework kind. Mismatches contribute nothing; matches score
// at full confidence because Kind is a content-addressable property,
// not an inferred attribute (SPEC §3.1 nominal family).
type Kindwise struct{}

// Kind returns "entity_kind" — the canonical anchor-kind string. The
// Pass-0.5-B DSL grammar and Pass-1 reverse index both reference this
// literal; do not rename without coordinating both sides.
func (Kindwise) Kind() string { return "entity_kind" }

// ConfidenceEntityKind is the score Kindwise assigns to an exact match.
// Kind equality is strong evidence — there is no fuzz between "Class"
// and "Interface" — so the anchor reports 1.0. The resolver still
// requires a higher-precision anchor (qualified_name, body_hash) to
// cross the bound threshold when a selector pins to a single entity.
const ConfidenceEntityKind = 1.0

// Evaluate scans every entity and returns those whose Kind equals the
// anchor's string value. Empty value → no matches; the resolver treats
// that as a cleanly-skipped rung. Results are sorted by qualified
// name for deterministic ordering across runs.
func (Kindwise) Evaluate(ctx context.Context, a *dsl.Anchor, store Lookup) ([]Match, error) {
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
		if string(e.Kind) != want {
			continue
		}
		out = append(out, matchFromEntity(e, ConfidenceEntityKind,
			fmt.Sprintf("entity_kind == %q", want)))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].QualifiedName < out[j].QualifiedName
	})
	return out, nil
}
