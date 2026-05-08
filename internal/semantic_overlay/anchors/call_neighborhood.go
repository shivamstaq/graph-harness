package anchors

import (
	"context"
	"fmt"
	"sort"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/dsl"
)

// CallNeighborhood matches functions/methods whose direct callers / callees
// in the code.core adjacency graph overlap with the anchor's hint sets.
//
// Scoring (per anchor):
//
//	both callers and callees overlap → ConfidenceCallNeighborBoth
//	either side overlaps             → ConfidenceCallNeighborOne
//	neither overlaps                 → no match
//
// The anchor stores hints as qualified-name strings; the evaluator pulls
// the candidate's adjacency-table neighbors and checks set overlap.
type CallNeighborhood struct{}

// Kind returns "call_neighborhood".
func (CallNeighborhood) Kind() string { return "call_neighborhood" }

// Evaluate scans every function/method and reports those whose neighborhood
// overlaps the hint set.
func (CallNeighborhood) Evaluate(ctx context.Context, a *dsl.Anchor, store Lookup) ([]Match, error) {
	if a == nil || a.Value == nil || a.Value.Neighbor == nil {
		return nil, nil
	}
	cn := a.Value.Neighbor
	wantCallers := stringSet(neighborItems(cn.Callers))
	wantCallees := stringSet(neighborItems(cn.Callees))
	if len(wantCallers) == 0 && len(wantCallees) == 0 {
		return nil, nil
	}
	funcs, err := store.LookupFunctions(ctx)
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, e := range funcs {
		callerHit, calleeHit := false, false
		if len(wantCallers) > 0 {
			names, err := store.CallerNames(ctx, e.ID)
			if err != nil {
				return nil, err
			}
			callerHit = anyOverlap(names, wantCallers)
		}
		if len(wantCallees) > 0 {
			names, err := store.CalleeNames(ctx, e.ID)
			if err != nil {
				return nil, err
			}
			calleeHit = anyOverlap(names, wantCallees)
		}
		switch {
		case callerHit && calleeHit:
			out = append(out, matchFromEntity(e, ConfidenceCallNeighborBoth,
				detailNeighborhood(cn, true, true)))
		case callerHit || calleeHit:
			// If only one side is declared, partial-match is still both-sides
			// satisfied. Score depends on whether both sides were declared.
			oneSideOnly := len(wantCallers) == 0 || len(wantCallees) == 0
			conf := ConfidenceCallNeighborOne
			if oneSideOnly {
				conf = ConfidenceCallNeighborBoth
			}
			out = append(out, matchFromEntity(e, conf,
				detailNeighborhood(cn, callerHit, calleeHit)))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
	return out, nil
}

func neighborItems(s *dsl.StringList) []string {
	if s == nil {
		return nil
	}
	return s.Items
}

func stringSet(in []string) map[string]struct{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(in))
	for _, s := range in {
		out[s] = struct{}{}
	}
	return out
}

func anyOverlap(observed []string, want map[string]struct{}) bool {
	for _, n := range observed {
		if _, ok := want[n]; ok {
			return true
		}
	}
	return false
}

func detailNeighborhood(cn *dsl.CallNeighborhood, callerHit, calleeHit bool) string {
	switch {
	case callerHit && calleeHit:
		return fmt.Sprintf("call_neighborhood: %d caller hint(s) + %d callee hint(s) matched",
			len(neighborItems(cn.Callers)), len(neighborItems(cn.Callees)))
	case callerHit:
		return fmt.Sprintf("call_neighborhood: %d caller hint(s) matched", len(neighborItems(cn.Callers)))
	case calleeHit:
		return fmt.Sprintf("call_neighborhood: %d callee hint(s) matched", len(neighborItems(cn.Callees)))
	}
	return "call_neighborhood: no overlap"
}

// Compile-time guard: the evaluator package depends on code_core.Entity but
// must not import code_core directly here — Lookup is the seam. Reference
// the type to keep godoc cross-links working.
var _ = code_core.Entity{}
