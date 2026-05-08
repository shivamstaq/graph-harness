package lsp

// Minimal subset of the LSP wire types we consume. We intentionally
// avoid pulling go.lsp.dev/protocol so the dependency surface stays
// small; the subset is well-defined by the LSP base protocol spec
// (textDocument/{documentSymbol,definition,references}).

// position is the LSP zero-based line/character pair.
type position struct {
	Line      uint32 `json:"line"`
	Character uint32 `json:"character"`
}

// rangeT is the LSP Range struct (zero-based, end-exclusive).
type rangeT struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

// location is the LSP Location returned by definition/references.
type location struct {
	URI   string `json:"uri"`
	Range rangeT `json:"range"`
}

// documentSymbol is the LSP DocumentSymbol nested representation.
// Children may recurse arbitrarily; we walk the tree to flatten
// methods/closures and assign parent_id + ordinal per §6.12.
type documentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           int              `json:"kind"`
	Range          rangeT           `json:"range"`
	SelectionRange rangeT           `json:"selectionRange"`
	Children       []documentSymbol `json:"children,omitempty"`
}

// LSP SymbolKind enum (subset). Values are the canonical integer
// codes from the LSP spec.
const (
	lspKindFile      = 1
	lspKindModule    = 2
	lspKindClass     = 5
	lspKindMethod    = 6
	lspKindProperty  = 7
	lspKindField     = 8
	lspKindConstr    = 9 //nolint:unused,deadcode,varcheck
	lspKindEnum      = 10
	lspKindInterface = 11
	lspKindFunction  = 12
	lspKindVariable  = 13
	lspKindConstant  = 14
	lspKindStruct    = 23
)

// initializeParams is the minimal subset we send. capabilities is a
// permissive empty map — we let the server pick defaults.
type initializeParams struct {
	ProcessID    int            `json:"processId"`
	RootURI      string         `json:"rootUri"`
	Capabilities map[string]any `json:"capabilities"`
}

// referencesParams is textDocument/references input.
type referencesParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     position               `json:"position"`
	Context      referenceContext       `json:"context"`
}

type referenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

type textDocumentPositionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     position               `json:"position"`
}

type documentSymbolParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}
