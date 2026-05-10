package scip

import (
	"strings"

	"github.com/shivamstaq/graph-harness/internal/source_live"
	"github.com/shivamstaq/graph-harness/internal/source_live/scip/proto"
)

// NewGoImporter returns the scip-go importer. scip-go encodes the Go
// package path as a single namespace descriptor whose name carries the
// full module-relative path (e.g. namespace
// "example.com/checkout/internal/checkout" → Type "CheckoutValidator"
// → Method "Validate"). Tree-sitter and gopls produce qualified names
// keyed on the leaf-package basename only ("checkout.CheckoutValidator
// .Validate"). Without an override the §6.12 canonical IDs would
// diverge across sources and the unifier would materialise the same
// logical entity twice. goQualifyOverride trims module-path namespace
// descriptors to their last `/`-segment so all three sources agree.
func NewGoImporter() Importer { return &goImporter{} }

type goImporter struct{}

// Language returns "go".
func (g *goImporter) Language() string { return "go" }

// Import implements Importer for SCIP-Go indexes.
func (g *goImporter) Import(idx *proto.Index, pathFilter string) []source_live.Symbol {
	if idx == nil {
		return nil
	}
	out := make([]source_live.Symbol, 0)
	for _, doc := range idx.Documents {
		if doc == nil {
			continue
		}
		if doc.Language != "" && doc.Language != "go" && doc.Language != "Go" {
			continue
		}
		if !matchPath(doc, pathFilter) {
			continue
		}
		out = append(out, importDocument(doc, "go", "extractor:scip:scip-go", 0.95, goQualifyOverride)...)
	}
	return out
}

// goQualifyOverride collapses scip-go namespace descriptors that carry
// a module-relative path ("example.com/checkout/internal/checkout") to
// their leaf segment ("checkout"). This is what tree-sitter's
// parser_go.go and our gopls driver both use as the package-prefix
// component of the qualified name; without this rewrite the §6.12
// canonical ID computed from scip-go entities cannot match the IDs
// from the other two sources, and three-source unification falls
// apart for every Go entity.
//
// Returns ("", "", "") when no namespace descriptor needs trimming —
// the shared importDocument path then uses the default qualifiedName
// / methodReceiver inference.
func goQualifyOverride(p *parsedSymbol, _ string) (qn, recv string, kind source_live.SymbolKind) {
	if p == nil || len(p.descriptors) == 0 {
		return "", "", ""
	}
	needsTrim := false
	for _, d := range p.descriptors {
		if d.suffix == suffixNamespace && strings.Contains(d.name, "/") {
			needsTrim = true
			break
		}
	}
	if !needsTrim {
		return "", "", ""
	}
	trimmed := make([]descriptor, len(p.descriptors))
	for i, d := range p.descriptors {
		trimmed[i] = d
		if d.suffix == suffixNamespace && strings.Contains(d.name, "/") {
			if idx := strings.LastIndex(d.name, "/"); idx >= 0 {
				trimmed[i].name = d.name[idx+1:]
			}
		}
	}
	patched := &parsedSymbol{descriptors: trimmed}
	return patched.qualifiedName(), patched.methodReceiver(), ""
}
