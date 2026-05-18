package source_live

// This file is the published cross-source contract. LSP, SCIP, and
// tree-sitter extractors all emit []Symbol values with identical
// semantics so that code.core can compute the same content-addressable
// canonical key from each source independently and unify them via the
// three-source rule (SPEC §6.11, §6.12, §6.15).
//
// The envelope shape is intentionally minimal: every field is either
// required to compute one of the canonical-key formulas in §6.12, or
// required to record provenance.sources[] in code.core. New fields are
// additions only; renames or removals are a coordinated multi-team
// change because both T-extractors (producers) and T-core (consumer)
// read this struct.

// SourceClass tags which fact source produced a Symbol. The values are
// the canonical strings used in code.core provenance.sources[].source_class
// per SPEC §6.11 (LSP=live, SCIP=index, tree-sitter=structural).
type SourceClass string

// Source-class constants. Keep aligned with manifests/source.live.yaml
// trust.direct_writes_from and code.core provenance schema.
const (
	SourceClassLSP        SourceClass = "live_lsp"
	SourceClassSCIP       SourceClass = "index_scip"
	SourceClassTreesitter SourceClass = "structural_treesitter"
)

// SymbolKind enumerates the canonical entity kinds extractors emit.
// The taxonomy mirrors code.core.EntityKind plus the catch-all and
// anonymous variants required for SPEC §6.12 identity. Adding a new
// kind requires adding the corresponding identity-formula branch in
// internal/code_core.
type SymbolKind string

// Canonical symbol kinds. The string values are stable on the wire
// and on disk: changing them is a migration, not a refactor.
const (
	SymbolKindFile      SymbolKind = "File"
	SymbolKindModule    SymbolKind = "Module"
	SymbolKindFunction  SymbolKind = "Function"
	SymbolKindMethod    SymbolKind = "Method"
	SymbolKindTypeDecl  SymbolKind = "TypeDecl"
	SymbolKindClass     SymbolKind = "Class"
	SymbolKindInterface SymbolKind = "Interface"
	SymbolKindClosure   SymbolKind = "Closure" // anonymous fn/lambda
	SymbolKindSymbol    SymbolKind = "Symbol"  // catch-all
)

// Range is a byte+line position pair within the originating source
// file. Lines are 1-based to match LSP/SCIP and tree-sitter human-
// readable diagnostics; bytes are 0-based to match Go slice semantics.
type Range struct {
	StartByte uint32
	EndByte   uint32
	StartLine uint32 // 1-based
	EndLine   uint32 // 1-based
}

// Symbol is the unified envelope every source.live extractor emits
// alongside its raw fact event. Identical Symbol values must be
// produced by all three sources for the same source-level entity so
// that the canonical content-addressable key (SPEC §6.12) collapses
// to the same hash regardless of which extractor saw it first.
//
// Field rationale:
//
//   - Name + QualifiedName: identity inputs for Function / Method /
//     TypeDecl / Class / Interface (§6.12 formulas). Name is the local
//     identifier (e.g. "Validate"); QualifiedName is the dotted path
//     including package/module + receiver (e.g. "checkout.Validator.Validate").
//   - Kind: selects which identity formula applies.
//   - Range: enables anchor evaluators (body_hash, ast_hash) and lets
//     the resolver point UI surfaces at the source location.
//   - Signature: feeds the per-language signature normalizer; required
//     for Function/Method identity and the function_signature anchor.
//   - LanguageID: prevents cross-language name collisions in polyglot
//     repos (§6.12). Canonical values: "go", "typescript", "python".
//   - Path: workspace-relative file path; required for FileID and
//     path_glob anchor evaluation. Always populated, even on Symbols
//     produced by SCIP indexers that historically used document URIs.
//   - Receiver: methods only; the qualified name of the enclosing type.
//   - BodyHash: hex sha256 of the function/method body bytes; feeds
//     the body_hash anchor evaluator and the rename-as-supersession
//     fingerprint match in history.evolution.
//   - ParentID + Ordinal: anonymous-symbol identity per §6.12
//     (closures, lambdas, anonymous structs/classes). ParentID is the
//     code.core canonical ID of the enclosing symbol; Ordinal is the
//     0-based position among same-kind anonymous siblings.
//   - SourceClass + ProducedBy: provenance.sources[] inputs. SourceClass
//     is the unification key; ProducedBy is the human-readable
//     extractor identifier ("extractor:lsp:gopls", "extractor:scip",
//     "extractor:treesitter:go").
//   - Confidence: 0..1 confidence the source assigns to this Symbol.
//     Used by the conflict-as-event rule when sources disagree
//     (lower-confidence claim is still recorded but flagged).
//
// Optional fields use the zero value to mean "not applicable" rather
// than introducing pointer-typed slots (Go zero-value semantics keep
// the struct cheap to construct from extractors and easy to compare
// in tests). Receiver/BodyHash/ParentID are empty strings; Ordinal is
// zero (anonymous siblings start at 1, so 0 means N/A).
type Symbol struct {
	Name          string
	QualifiedName string
	Kind          SymbolKind
	Range         Range
	Signature     string
	LanguageID    string
	Path          string
	Receiver      string
	BodyHash      string
	ParentID      string
	Ordinal       uint32
	SourceClass   SourceClass
	ProducedBy    string
	Confidence    float64

	// SymbolFingerprint and ASTHash are per-symbol stable hashes
	// used by the selector resolver's symbol_fingerprint and ast_hash
	// anchor evaluators (SPEC §3.3, P1.T25). Computed by tree-sitter
	// parsers (F11) and copied through to the code.core Entity row.
	// Empty strings mean "not computed by this extractor"; the
	// resolver falls back to the next anchor on the ladder.
	SymbolFingerprint string
	ASTHash           string
}

// IsAnonymous reports whether the Symbol identifies an anonymous entity
// (closure, lambda, anonymous struct/class). For these, identity per
// §6.12 derives from ParentID + KindTag + Ordinal rather than from
// QualifiedName + Signature. Extractors set ParentID and Ordinal
// when emitting anonymous symbols and leave QualifiedName empty.
func (s Symbol) IsAnonymous() bool {
	return s.QualifiedName == "" && s.ParentID != ""
}

// KindTag returns the §6.12 kind_tag string used in identity hashing.
// For now the kind_tag is the SymbolKind string value verbatim;
// keeping a dedicated accessor lets us evolve the tag taxonomy
// (e.g. distinct tags for "ArrowFunction" vs "FunctionExpression"
// in TypeScript) without breaking the Symbol API.
func (s Symbol) KindTag() string {
	return string(s.Kind)
}
