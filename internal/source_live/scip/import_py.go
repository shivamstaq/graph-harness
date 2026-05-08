package scip

import (
	"github.com/shivamstaq/graph-harness/internal/source_live"
	"github.com/shivamstaq/graph-harness/internal/source_live/scip/proto"
)

// NewPyImporter returns the scip-python importer.
//
// scip-python encodes module paths via Namespace descriptors using
// dotted names (matching Python's import semantics). The shared
// qualified-name reconstruction handles that correctly. Pyright-
// emitted SCIP indexes sometimes report Term-suffixed symbols for
// what we'd call Functions; the importer below normalises those.
func NewPyImporter() Importer { return &pyImporter{} }

type pyImporter struct{}

// Language returns "python".
func (p *pyImporter) Language() string { return "python" }

// Import implements Importer for SCIP-Python indexes.
func (p *pyImporter) Import(idx *proto.Index, pathFilter string) []source_live.Symbol {
	if idx == nil {
		return nil
	}
	out := make([]source_live.Symbol, 0)
	for _, doc := range idx.Documents {
		if doc == nil {
			continue
		}
		if doc.Language != "" && doc.Language != "python" && doc.Language != "Python" {
			continue
		}
		if !matchPath(doc, pathFilter) {
			continue
		}
		out = append(out, importDocument(doc, "python", "extractor:scip:scip-python", 0.95, pyQualifyOverride)...)
	}
	return out
}

// pyQualifyOverride bumps Term-suffixed symbols whose disambiguator
// matches a function-call pattern up to Function kind. Pyright emits
// `mod/foo.` (Term suffix) for module-level functions; the SCIP
// SymbolInformation.Kind field would say Function, but older pyright
// builds skip that field. The override keeps unification with LSP /
// tree-sitter symmetric.
func pyQualifyOverride(p *parsedSymbol, displayName string) (qn, recv string, kind source_live.SymbolKind) {
	_ = displayName
	if p == nil {
		return "", "", ""
	}
	if p.terminalSuffix() == suffixTerm && p.terminalName() != "" {
		// Heuristic: a Term whose name starts with a lowercase letter
		// or underscore at module scope is most likely a function.
		// Class names use CamelCase by convention; constants use
		// UPPER_CASE. This matches PEP 8 and aligns with what
		// tree-sitter reports.
		first := p.terminalName()[0]
		isFunctionLike := first == '_' || (first >= 'a' && first <= 'z')
		if isFunctionLike {
			return "", "", source_live.SymbolKindFunction
		}
	}
	return "", "", ""
}
