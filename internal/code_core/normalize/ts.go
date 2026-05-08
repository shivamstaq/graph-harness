package normalize

// TS canonicalizes a TypeScript function signature for code.core
// identity.
//
// Concretely it:
//
//   - collapses whitespace and tightens commas the same way [Go] does;
//   - rewrites a leading type-parameter block of the form `<T extends X,
//     U = number>(...)` so the parameter names become `T0, T1, ...` in
//     declaration order, with the canonical names substituted into the
//     rest of the signature.
//
// The function deliberately does not strip parameter names from value
// parameters: TypeScript signatures retain names in source and all three
// fact sources (LSP, SCIP, tree-sitter) emit the same names for the
// same source byte range. Stripping would diverge from the on-disk
// shape and confuse the body_hash / function_signature anchor pair.
//
// Limitations of the v1 implementation:
//
//   - inline object types (`{ a: number; b: string }`) are not
//     re-sorted by member; sorting could lose declaration order that
//     downstream tooling occasionally relies on (overload resolution).
//     Disagreement here surfaces as code.core.SymbolDisambiguation.
//   - conditional / mapped types are passed through unchanged.
func TS(sig string) string {
	if sig == "" {
		return sig
	}
	out := canonicalizeWhitespace(sig)
	out = canonicalizeGenerics(out, '<', '>')
	return out
}
