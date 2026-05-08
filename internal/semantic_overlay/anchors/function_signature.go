package anchors

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/shivamstaq/graph-harness/internal/code_core/normalize"
	"github.com/shivamstaq/graph-harness/internal/dsl"
)

// FunctionSignature matches a Function/Method entity whose normalized
// signature equals the anchor's `sig(<params> -> <return>)` literal,
// after running both through the per-language normalizer (SPEC §6.12).
//
// Matching algorithm:
//
//  1. Render the anchor's FunctionSig payload to a canonical string.
//  2. For each Function/Method entity, run the entity's stored
//     `normalized_signature` through the per-language normalizer (no-op if
//     already normalized) and compare.
//  3. Equal → match at [ConfidenceFunctionSig].
//
// The anchor side passes through the same normalize.ForLanguage path as the
// entity side, so authored signatures written in canonical form match
// entities ingested via any of the three fact sources.
type FunctionSignature struct{}

// Kind returns "function_signature".
func (FunctionSignature) Kind() string { return "function_signature" }

// Evaluate scans all functions and returns those whose normalized
// signature equals the anchor's signature literal.
func (FunctionSignature) Evaluate(ctx context.Context, a *dsl.Anchor, store Lookup) ([]Match, error) {
	if a == nil || a.Value == nil || a.Value.Sig == nil {
		return nil, nil
	}
	want := renderFunctionSig(a.Value.Sig)
	if want == "" {
		return nil, nil
	}
	funcs, err := store.LookupFunctions(ctx)
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, e := range funcs {
		if e.NormalizedSignature == "" {
			continue // entity ingested without a signature; can't compare
		}
		entSig := normalize.ForLanguage(e.LanguageID, e.NormalizedSignature)
		anchorSig := normalize.ForLanguage(e.LanguageID, want)
		if entSig == anchorSig {
			out = append(out, matchFromEntity(e, ConfidenceFunctionSig,
				fmt.Sprintf("function_signature %s", want)))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
	return out, nil
}

// renderFunctionSig produces the canonical pre-normalize string form
// `sig(P1, P2) -> R` or `sig(P1, P2) -> (R1, R2)` from the parsed AST.
// The downstream normalizer collapses whitespace + canonicalizes generics,
// so the textual form here is already close to its normalized output.
func renderFunctionSig(s *dsl.FunctionSig) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("sig(")
	b.WriteString(strings.Join(s.Params, ", "))
	b.WriteString(") -> ")
	if s.Return == nil {
		return b.String()
	}
	switch {
	case s.Return.Single != nil:
		b.WriteString(*s.Return.Single)
	case s.Return.Tuple != nil:
		b.WriteString("(")
		b.WriteString(strings.Join(s.Return.Tuple.Items, ", "))
		b.WriteString(")")
	}
	return b.String()
}
