package scip

import (
	"github.com/shivamstaq/graph-harness/internal/source_live"
	"github.com/shivamstaq/graph-harness/internal/source_live/scip/proto"
)

// NewGoImporter returns the scip-go importer. scip-go's symbol
// strings encode receiver types via a Type-suffixed descriptor
// immediately preceding the Method-suffixed one (e.g.
// `pkg/Validator#Validate().`); the shared importDocument path
// already handles that, so the Go importer is effectively a thin
// language-tag wrapper.
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
		out = append(out, importDocument(doc, "go", "extractor:scip:scip-go", 0.95, nil)...)
	}
	return out
}
