package scip

import (
	"github.com/shivamstaq/graph-harness/internal/source_live"
	"github.com/shivamstaq/graph-harness/internal/source_live/scip/proto"
)

// NewTSImporter returns the scip-typescript importer.
//
// scip-typescript symbol strings use namespace descriptors for module
// paths and Type/Term descriptors for class members. We carry through
// the default qualifyOverride; the only TS-specific tweak is that
// TypeScript reports anonymous default exports with empty terminal
// names — we coalesce those to "default" so the unification path has
// something stable to hash.
func NewTSImporter() Importer { return &tsImporter{} }

type tsImporter struct{}

// Language returns "typescript".
func (t *tsImporter) Language() string { return "typescript" }

// Import implements Importer for SCIP-TypeScript indexes.
func (t *tsImporter) Import(idx *proto.Index, pathFilter string) []source_live.Symbol {
	if idx == nil {
		return nil
	}
	out := make([]source_live.Symbol, 0)
	for _, doc := range idx.Documents {
		if doc == nil {
			continue
		}
		if !matchTSLanguage(doc.Language) {
			continue
		}
		if !matchPath(doc, pathFilter) {
			continue
		}
		out = append(out, importDocument(doc, "typescript", "extractor:scip:scip-typescript", 0.95, tsQualifyOverride)...)
	}
	return out
}

func matchTSLanguage(lang string) bool {
	if lang == "" {
		return true
	}
	switch lang {
	case "typescript", "TypeScript", "TSX", "tsx", "javascript", "JavaScript", "JSX", "jsx":
		return true
	}
	return false
}

// tsQualifyOverride coalesces anonymous default exports into a
// "default" terminal name so the symbol stays addressable. Returns
// empty values when no override is needed (the shared path then uses
// the default qualifiedName/receiver inference).
func tsQualifyOverride(p *parsedSymbol, displayName string) (qn, recv string, kind source_live.SymbolKind) {
	if p == nil {
		return "", "", ""
	}
	if displayName == "" && p.terminalName() == "" && len(p.descriptors) > 0 {
		// Replace the trailing empty descriptor with "default".
		descs := append([]descriptor{}, p.descriptors...)
		descs[len(descs)-1].name = "default"
		patched := &parsedSymbol{descriptors: descs}
		return patched.qualifiedName(), patched.methodReceiver(), ""
	}
	return "", "", ""
}
