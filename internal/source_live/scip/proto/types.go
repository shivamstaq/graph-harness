package proto

// SCIP message types — Go projection of the subset of scip.proto we
// consume. Field numbers in struct comments correspond to the proto
// schema (scip.proto in this directory). Keep them in lockstep with
// the canonical schema.

// Index is the top-level SCIP container.
type Index struct {
	Metadata        *Metadata            // field 1
	Documents       []*Document          // field 2
	ExternalSymbols []*SymbolInformation // field 3
}

// Metadata describes the index's tooling and layout.
type Metadata struct {
	ProjectRoot string // field 3
}

// Document is one source file's worth of facts.
type Document struct {
	RelativePath string               // field 1
	Occurrences  []*Occurrence        // field 2
	Symbols      []*SymbolInformation // field 3
	Language     string               // field 4
}

// SymbolInformation holds symbol-level metadata. We only consume the
// identity (symbol id), kind, and display name — documentation +
// signature_documentation are skipped to keep the decoder small.
type SymbolInformation struct {
	Symbol      string // field 1
	Kind        Kind   // field 5
	DisplayName string // field 6
}

// Occurrence ties a source range to a symbol id with role bits.
type Occurrence struct {
	Range       []int32 // field 1 — packed [startLine, startCol, endLine, endCol] or 3-elt
	Symbol      string  // field 2
	SymbolRoles int32   // field 3 — SymbolRole bitset
}

// Kind enumerates symbol kinds emitted by SCIP indexers. Values mirror
// scip.proto's SymbolInformation.Kind enum (a subset — we keep the
// kinds source.live consumes; the rest pass through as KindUnspecified).
type Kind int32

// Subset of SCIP SymbolInformation.Kind values. The exact integer
// codes come from scip.proto and are wire-stable.
const (
	KindUnspecified Kind = 0
	KindAbstractMtd Kind = 66
	KindClass       Kind = 7
	KindConstructor Kind = 9
	KindEnum        Kind = 11
	KindEnumMember  Kind = 12
	KindField       Kind = 15
	KindFile        Kind = 16
	KindFunction    Kind = 17
	KindInterface   Kind = 21
	KindMacro       Kind = 25
	KindMethod      Kind = 26
	KindMethodRecv  Kind = 27
	KindModule      Kind = 30 //nolint:unused // scip.proto value 30 is reserved for Module
	KindNamespace   Kind = 31
	KindObject      Kind = 32
	KindOperator    Kind = 33
	KindPackage     Kind = 34
	KindPackageObj  Kind = 35
	KindParameter   Kind = 36
	KindProperty    Kind = 37
	KindProtocol    Kind = 38
	KindStruct      Kind = 47
	KindSubscript   Kind = 48
	KindTrait       Kind = 51
	KindType        Kind = 53
	KindTypeAlias   Kind = 54
	KindTypeParam   Kind = 55
	KindUnion       Kind = 56
	KindValue       Kind = 57
	KindVariable    Kind = 58
)

// SymbolRole bits — Occurrence.symbol_roles bitset values. Matches the
// SymbolRole enum in scip.proto.
const (
	RoleDefinition        = 0x1
	RoleImport            = 0x2
	RoleWriteAccess       = 0x4
	RoleReadAccess        = 0x8
	RoleGenerated         = 0x10
	RoleTest              = 0x20
	RoleForwardDefinition = 0x40
)
