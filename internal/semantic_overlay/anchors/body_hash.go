package anchors

import (
	"context"
	"fmt"
	"sort"

	"github.com/shivamstaq/graph-harness/internal/dsl"
)

// BodyHash matches on `code.core.body_hash` exact equality (SPEC §3.1).
// The hash is language-blind, so this anchor is a strong signal for
// re-anchoring after a rename: the function body survives across rename
// edits even when the qualified name changes.
type BodyHash struct{}

// Kind returns "body_hash".
func (BodyHash) Kind() string { return "body_hash" }

// Evaluate returns every entity with the requested body_hash, scored at
// [ConfidenceBodyHash].
func (BodyHash) Evaluate(ctx context.Context, a *dsl.Anchor, store Lookup) ([]Match, error) {
	bh := stringValue(a)
	if bh == "" {
		return nil, nil
	}
	ents, err := store.LookupByBodyHash(ctx, bh)
	if err != nil {
		return nil, err
	}
	out := make([]Match, 0, len(ents))
	for _, e := range ents {
		out = append(out, matchFromEntity(e, ConfidenceBodyHash,
			fmt.Sprintf("body_hash == %q", bh)))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
	return out, nil
}
