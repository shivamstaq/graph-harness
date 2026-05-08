package scip

import (
	"github.com/shivamstaq/graph-harness/internal/source_live"
	"github.com/shivamstaq/graph-harness/internal/source_live/scip/proto"
)

// Importer translates a SCIP Index into source_live.Symbol envelopes
// for a single language. The per-language ImportGo / ImportTS /
// ImportPy variants only differ in symbol-string interpretation
// (qualified-name reconstruction quirks); the shared importDocument
// path handles the structure.
type Importer interface {
	// Language returns the canonical language id this importer emits.
	Language() string

	// Import translates an Index into a flat []Symbol for every
	// Document whose language matches Language(). When `path` is set,
	// only the matching document is imported (call site = SCIP
	// refresh restricted to a single edited file).
	Import(idx *proto.Index, pathFilter string) []source_live.Symbol
}

// importDocument is the shared entry point all per-language importers
// use. It handles the SymbolInformation → source_live.Symbol mapping
// and the DefinitionOccurrence → Range merge. languageID overrides
// the document's wire language when set (the per-language Importer
// passes its canonical id so we don't depend on whether the SCIP
// indexer wrote "go" or "Go" or "golang").
func importDocument(
	doc *proto.Document,
	languageID string,
	producedBy string,
	confidence float64,
	qualifyOverride func(p *parsedSymbol, displayName string) (qn, recv string, kind source_live.SymbolKind),
) []source_live.Symbol {
	if doc == nil {
		return nil
	}
	if languageID == "" {
		languageID = doc.Language
	}
	// Index occurrences by symbol id so we can pick the Definition
	// occurrence's range as each Symbol's authoritative position.
	defRange := make(map[string]source_live.Range, len(doc.Symbols))
	for _, occ := range doc.Occurrences {
		if occ == nil || occ.Symbol == "" {
			continue
		}
		if occ.SymbolRoles&proto.RoleDefinition == 0 {
			continue
		}
		if _, ok := defRange[occ.Symbol]; ok {
			continue
		}
		defRange[occ.Symbol] = scipRange(occ.Range)
	}
	out := make([]source_live.Symbol, 0, len(doc.Symbols))
	for _, info := range doc.Symbols {
		if info == nil || info.Symbol == "" {
			continue
		}
		parsed := parseSymbol(info.Symbol)
		var qn, recv string
		var kind source_live.SymbolKind
		if qualifyOverride != nil {
			qn, recv, kind = qualifyOverride(parsed, info.DisplayName)
		}
		if qn == "" {
			qn = parsed.qualifiedName()
		}
		if recv == "" {
			recv = parsed.methodReceiver()
		}
		if kind == "" {
			kind = scipKindToSymbolKind(info.Kind, parsed)
		}
		name := info.DisplayName
		if name == "" {
			name = parsed.terminalName()
		}
		sym := source_live.Symbol{
			Name:          name,
			QualifiedName: qn,
			Kind:          kind,
			Range:         defRange[info.Symbol],
			LanguageID:    languageID,
			Path:          doc.RelativePath,
			Receiver:      recv,
			SourceClass:   source_live.SourceClassSCIP,
			ProducedBy:    producedBy,
			Confidence:    confidence,
		}
		out = append(out, sym)
	}
	return out
}

// scipRange projects a SCIP packed-int32 range into source_live.Range.
// SCIP allows three- or four-element ranges; the three-elt variant
// shares startLine for both endpoints.
func scipRange(r []int32) source_live.Range {
	if len(r) == 0 {
		return source_live.Range{}
	}
	startLine := nonNegU32(r[0])
	endLine := startLine
	switch len(r) {
	case 4:
		endLine = nonNegU32(r[2])
	}
	return source_live.Range{
		StartLine: startLine + 1, // SCIP is 0-based, source_live is 1-based
		EndLine:   endLine + 1,
	}
}

// nonNegU32 clamps a possibly-negative int32 to zero before widening,
// avoiding the gosec G115 lint trip and matching SCIP's contract that
// line / character values are non-negative.
func nonNegU32(v int32) uint32 {
	if v < 0 {
		return 0
	}
	return uint32(v) //nolint:gosec
}

// scipKindToSymbolKind translates a SCIP Kind enum to our SymbolKind
// taxonomy. Falls back to terminal-suffix inference when the indexer
// didn't populate the kind field.
func scipKindToSymbolKind(k proto.Kind, parsed *parsedSymbol) source_live.SymbolKind {
	switch k {
	case proto.KindFile:
		return source_live.SymbolKindFile
	case proto.KindModule, proto.KindNamespace, proto.KindPackage:
		return source_live.SymbolKindModule
	case proto.KindClass, proto.KindStruct:
		return source_live.SymbolKindClass
	case proto.KindInterface, proto.KindProtocol, proto.KindTrait:
		return source_live.SymbolKindInterface
	case proto.KindMethod, proto.KindAbstractMtd, proto.KindConstructor:
		return source_live.SymbolKindMethod
	case proto.KindFunction:
		return source_live.SymbolKindFunction
	case proto.KindType, proto.KindTypeAlias, proto.KindEnum, proto.KindUnion:
		return source_live.SymbolKindTypeDecl
	}
	// Fall back to suffix inference for indexes that don't populate
	// SymbolInformation.kind.
	switch parsed.terminalSuffix() {
	case suffixMethod:
		return source_live.SymbolKindFunction
	case suffixType:
		return source_live.SymbolKindClass
	case suffixNamespace:
		return source_live.SymbolKindModule
	}
	return source_live.SymbolKindSymbol
}

// matchPath returns true if either `filter` is empty (= include all)
// or doc.RelativePath equals filter (= single-file refresh).
func matchPath(doc *proto.Document, filter string) bool {
	return filter == "" || doc.RelativePath == filter
}
