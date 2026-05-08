// Package normalize implements per-language signature normalizers for
// code.core identity (SPEC §6.12). A normalized signature is the input to
// the content-addressable Function/Method ID hash, so it must be:
//
//   - deterministic across reruns,
//   - stable under whitespace and comment edits,
//   - language-aware (Go generics, TS generics, Python generics + kw-only),
//   - cheap to compute (string-level, no full type inference).
//
// The package exposes a single dispatcher [ForLanguage] plus per-language
// entry points [Go], [TS], and [Python]. Each entry point is a pure
// function: no I/O, no globals, fully unit-testable.
//
// Per SPEC §6.12 normalization performs four steps:
//  1. strip formatting (collapse runs of whitespace, trim inside brackets),
//  2. normalize parameter naming (strip parameter names where they do not
//     contribute to the type signature — TS/Python today; Go retains names
//     because Go signatures are written name-then-type and parameter
//     elision changes how downstream tools render them),
//  3. canonicalize generic type-parameter names to T0, T1, ... in
//     declaration order,
//  4. sort named-parameter sets where a language's syntax permits unordered
//     keyword arguments (Python keyword-only block; TS object-type members).
//
// All three normalizers share a small whitespace canonicalization helper.
// The implementations are deliberately pragmatic: they handle the common
// cases that produce false-positive ID drift in three-source unification
// (LSP / SCIP / tree-sitter), while degrading gracefully on syntax that
// exceeds string-level analysis (e.g. Python `Annotated[X, ...]`, TS
// conditional types). When two extractors disagree on the canonical key
// for the same logical entity the unifier surfaces it as a
// code.core.SymbolDisambiguation event rather than papering over it here.
package normalize
