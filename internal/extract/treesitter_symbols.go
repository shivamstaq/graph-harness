package extract

import (
	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// ParsedFileToSymbols projects a tree-sitter ParsedFile into the
// uniform Symbol envelope so the unifier can fold tree-sitter facts
// alongside LSP and SCIP facts. Mirrors the per-language identity
// formula choices code_core.entityFromSymbol expects:
//
//   - functions emit SymbolKindFunction with QualifiedName + Signature.
//   - methods emit SymbolKindMethod with Receiver populated.
//
// SourceClass is always SourceClassTreesitter; ProducedBy carries the
// canonical extractor identifier so provenance.sources[] entries map
// 1:1 to what the watcher publishes. Confidence is 0.95 — high but not
// 1.0 because tree-sitter is structural, not type-aware (LSP/SCIP
// outrank it for the same canonical key).
func ParsedFileToSymbols(pf *source_live.ParsedFile) []source_live.Symbol {
	if pf == nil {
		return nil
	}
	out := make([]source_live.Symbol, 0, len(pf.Functions))
	for _, fn := range pf.Functions {
		kind := source_live.SymbolKindFunction
		if fn.Receiver != "" {
			kind = source_live.SymbolKindMethod
		}
		out = append(out, source_live.Symbol{
			Name:          fn.Name,
			QualifiedName: fn.QualifiedName,
			Kind:          kind,
			Range: source_live.Range{
				StartByte: fn.StartByte,
				EndByte:   fn.EndByte,
				StartLine: fn.StartLine,
				EndLine:   fn.EndLine,
			},
			Signature:   fn.Signature,
			LanguageID:  pf.Language,
			Path:        pf.Path,
			Receiver:    fn.Receiver,
			BodyHash:    fn.BodyHash,
			SourceClass: source_live.SourceClassTreesitter,
			ProducedBy:  "extractor:treesitter",
			Confidence:  0.95,
		})
	}
	return out
}
