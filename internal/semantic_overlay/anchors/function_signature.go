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
// signature is shape-equivalent to the anchor's signature literal.
//
// Comparison uses a structural form: each side is parsed into a list of
// parameter types + a return type, then compared as tuples. This bridges
// the gap between the SPEC §11.2 selector surface (`sig(T1, T2) -> R`,
// types only) and the per-language entity NormalizedSignature
// (Go: `(name1 T1, name2 T2) R`, TS: `(name1: T1, name2: T2) => R`,
// Python: `(name1: T1, name2: T2) -> R`). The anchor side is rendered
// directly from its FunctionSig AST; the entity side is parsed from its
// stored NormalizedSignature using the language's shape rules.
func (FunctionSignature) Evaluate(ctx context.Context, a *dsl.Anchor, store Lookup) ([]Match, error) {
	if a == nil || a.Value == nil || a.Value.Sig == nil {
		return nil, nil
	}
	wantParams, wantReturn := anchorSigShape(a.Value.Sig)
	if wantParams == nil && wantReturn == "" {
		return nil, nil
	}
	funcs, err := store.LookupFunctions(ctx)
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, e := range funcs {
		if e.NormalizedSignature == "" {
			continue
		}
		entParams, entReturn := parseEntitySignature(e.LanguageID, e.NormalizedSignature)
		if !sigShapeEqual(wantParams, wantReturn, entParams, entReturn, e.LanguageID) {
			continue
		}
		out = append(out, matchFromEntity(e, ConfidenceFunctionSig,
			fmt.Sprintf("function_signature %s", renderFunctionSig(a.Value.Sig))))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
	return out, nil
}

// anchorSigShape renders the anchor's parsed FunctionSig into a list of
// parameter types + a return type. Identifiers are taken verbatim — the
// per-language normalizer at compare time canonicalizes whitespace etc.
func anchorSigShape(s *dsl.FunctionSig) ([]string, string) {
	if s == nil {
		return nil, ""
	}
	params := append([]string(nil), s.Params...)
	ret := ""
	if s.Return != nil {
		switch {
		case s.Return.Single != nil:
			ret = *s.Return.Single
		case s.Return.Tuple != nil:
			parts := strings.Join(s.Return.Tuple.Items, ", ")
			ret = "(" + parts + ")"
		}
	}
	return params, ret
}

// parseEntitySignature extracts (paramTypes, returnType) from the entity's
// stored NormalizedSignature using language-specific shape rules. Per-
// language normalizer.ForLanguage is run on the raw string first to fold
// whitespace + canonicalize generics before parsing.
//
// Supported shapes:
//
//	go : `(name1 T1, name2 T2) R`           or `(name1 T1, name2 T2) (R1, R2)`
//	ts : `(name1: T1, name2: T2) => R`      or just `(...)` for a class method
//	py : `(name1: T1, name2: T2) -> R`
//
// Edge cases (e.g. variadic Go `...T`, TS rest `...T[]`, Python `*args`)
// pass through verbatim — comparison still works as long as the anchor
// uses the same shape.
func parseEntitySignature(languageID, raw string) ([]string, string) {
	canon := normalize.ForLanguage(languageID, raw)
	canon = strings.TrimSpace(canon)
	if !strings.HasPrefix(canon, "(") {
		return nil, ""
	}
	depth := 0
	closeIdx := -1
	for i, r := range canon {
		if r == '(' {
			depth++
			continue
		}
		if r == ')' {
			depth--
			if depth == 0 {
				closeIdx = i
				break
			}
		}
	}
	if closeIdx < 0 {
		return nil, ""
	}
	paramsBlob := canon[1:closeIdx]
	tail := strings.TrimSpace(canon[closeIdx+1:])
	switch normalizeLangAlias(languageID) {
	case "ts":
		tail = strings.TrimPrefix(tail, "=>")
	case "py":
		tail = strings.TrimPrefix(tail, "->")
	}
	tail = strings.TrimSpace(tail)
	return splitParamTypes(paramsBlob, languageID), tail
}

// splitParamTypes returns the list of TYPE strings for a comma-separated
// parameter blob, respecting balanced brackets/parens for generic and
// tuple types. For Go: `name type` → take `type`; for TS/Py:
// `name: type` → take `type`.
func splitParamTypes(blob, languageID string) []string {
	if strings.TrimSpace(blob) == "" {
		return nil
	}
	parts := splitTopLevelCommas(blob)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, extractParamType(p, languageID))
	}
	return out
}

// splitTopLevelCommas splits s on commas that are at depth 0 in
// `() / [] / <> / {}` brackets.
func splitTopLevelCommas(s string) []string {
	var (
		out []string
		buf strings.Builder
		dp  int
		db  int
		da  int
		dc  int
	)
	for _, r := range s {
		switch r {
		case '(':
			dp++
		case ')':
			dp--
		case '[':
			db++
		case ']':
			db--
		case '<':
			da++
		case '>':
			da--
		case '{':
			dc++
		case '}':
			dc--
		}
		if r == ',' && dp == 0 && db == 0 && da == 0 && dc == 0 {
			out = append(out, buf.String())
			buf.Reset()
			continue
		}
		buf.WriteRune(r)
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	return out
}

// extractParamType plucks the type substring out of one parameter
// declaration. Language conventions:
//
//	go : `name type`            → type is the substring after the last
//	                              top-level whitespace
//	ts : `name: type` or `type` → type is whatever follows the colon, or
//	                              the whole string if no colon
//	py : `name: type`           → same as ts
func extractParamType(p, languageID string) string {
	switch normalizeLangAlias(languageID) {
	case "ts", "py":
		if i := strings.Index(p, ":"); i >= 0 {
			return strings.TrimSpace(p[i+1:])
		}
		return strings.TrimSpace(p)
	default:
		// Go: split on top-level whitespace; the type is the last token.
		// `name type` → type. `type` (no name) → type.
		idx := lastTopLevelSpace(p)
		if idx < 0 {
			return strings.TrimSpace(p)
		}
		return strings.TrimSpace(p[idx+1:])
	}
}

// lastTopLevelSpace returns the byte index of the last whitespace
// character at depth 0 in s; -1 if none.
func lastTopLevelSpace(s string) int {
	dp, db, da, dc := 0, 0, 0, 0
	last := -1
	for i, r := range s {
		switch r {
		case '(':
			dp++
		case ')':
			dp--
		case '[':
			db++
		case ']':
			db--
		case '<':
			da++
		case '>':
			da--
		case '{':
			dc++
		case '}':
			dc--
		}
		if (r == ' ' || r == '\t') && dp == 0 && db == 0 && da == 0 && dc == 0 {
			last = i
		}
	}
	return last
}

// sigShapeEqual compares two parsed signatures. Equality requires:
//
//   - len(params) matches,
//   - each param's type matches after running through normalize.ForLanguage,
//   - the return type matches (after the same normalization).
func sigShapeEqual(wantParams []string, wantReturn string, gotParams []string, gotReturn string, languageID string) bool {
	if len(wantParams) != len(gotParams) {
		return false
	}
	for i := range wantParams {
		if normalize.ForLanguage(languageID, wantParams[i]) !=
			normalize.ForLanguage(languageID, gotParams[i]) {
			return false
		}
	}
	return normalize.ForLanguage(languageID, wantReturn) ==
		normalize.ForLanguage(languageID, gotReturn)
}

// normalizeLangAlias mirrors the alias table used elsewhere in the
// package. Duplicated locally to keep function_signature.go free of
// cross-file globals.
func normalizeLangAlias(id string) string {
	low := make([]byte, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		low[i] = c
	}
	switch string(low) {
	case "go", "golang":
		return "go"
	case "ts", "typescript", "javascript", "js", "tsx", "jsx":
		return "ts"
	case "py", "python":
		return "py"
	}
	return string(low)
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
