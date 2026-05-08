package normalize

// Go canonicalizes a Go function signature for code.core identity.
//
// Concretely it:
//
//   - collapses runs of whitespace, trims leading/trailing whitespace,
//     drops whitespace inside any bracket pair and after any comma so
//     that `(  a int ,  b   string ) error` and `(a int,b string) error`
//     produce identical output;
//   - rewrites a leading type-parameter block of the form `[T any, U
//     comparable](...)` so that the parameter names become `T0, T1, ...`
//     in declaration order, with the canonical names substituted into
//     the rest of the signature as whole-word identifiers (constraints
//     and parameter types stay verbatim).
//
// The function is pure. It does not parse Go — it operates on the
// signature substring as the tree-sitter parser already isolates it,
// which keeps the cost flat and the rules auditable. Per SPEC §6.12 the
// function does not strip parameter names, because Go signatures are
// universally rendered name-then-type and downstream consumers
// (selectors, history.evolution diffs) depend on that shape.
//
// Replaces the Phase 0 NormalizeGoSignature in identity.go (P0.T21).
func Go(sig string) string {
	if sig == "" {
		return sig
	}
	out := canonicalizeWhitespace(sig)
	out = canonicalizeGenerics(out, '[', ']')
	return out
}
