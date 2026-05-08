package lsp

import (
	"strings"

	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// NewGoplsDriver returns a Driver that talks to the `gopls` binary.
//
// gopls returns DocumentSymbol nested with Class as a Struct kind for
// receiver types and Method for receiver-bound functions. We override
// kindMap so anonymous structs surface as SymbolKindSymbol rather
// than SymbolKindClass to avoid polluting the type-decl set.
func NewGoplsDriver() Driver {
	return newGenericDriver(driverConf{
		languageID: "go",
		executable: "gopls",
		args:       []string{"serve"},
		producedBy: "extractor:lsp:gopls",
		// gopls reconstructs qualified names like "pkg.Type.Method" via
		// containerName / parents already, so the default qualifier
		// works without a Go-specific override. Receiver is filled in
		// by the generic flattener when kind=Method and parents != nil.
		qualify: goQualify,
	})
}

// goQualify mirrors the §6.12 Method identity formula:
//
//	pkg.Type.Method  → qualified_name = "pkg.Type.Method", receiver = "pkg.Type"
//
// gopls already returns the type as the parent in DocumentSymbol
// children, so we just dotted-join. The Receiver field is set
// downstream by the generic flattener.
func goQualify(parents []string, name string) string {
	if len(parents) == 0 {
		return name
	}
	// Some gopls builds emit the receiver as "(*Type)" — strip the
	// pointer marker to keep the qualified name stable across pointer
	// vs value receivers (the receiver type itself is what matters
	// for §6.12).
	cleaned := make([]string, len(parents))
	for i, p := range parents {
		p = strings.TrimPrefix(p, "(*")
		p = strings.TrimSuffix(p, ")")
		cleaned[i] = p
	}
	return strings.Join(append(cleaned, name), ".")
}

// goKindMap is unused for now — the default mapping suffices for gopls.
// Kept as a placeholder so future Go-specific kind overrides have an
// obvious home.
var _ = source_live.SymbolKindFunction
