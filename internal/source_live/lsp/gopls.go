package lsp

import (
	"regexp"
	"strings"

	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// NewGoplsDriver returns a Driver that talks to the `gopls` binary.
//
// gopls's textDocument/documentSymbol response shape is unusual for
// methods: instead of nesting Method symbols under their receiver
// type's Class symbol, gopls returns flat top-level Method entries
// whose name is the full Go method designator — `(*CheckoutValidator).Validate`
// for pointer receivers, `CheckoutValidator.Validate` for value
// receivers. The receiver type is also returned as a separate
// top-level Class symbol.
//
// goPostProcessSymbol normalises that shape so the §6.12 canonical
// MethodID hash matches what the tree-sitter parser produces:
//   - Receiver = the type name with `*` and parens stripped.
//   - Name     = the bare method name.
//   - QualifiedName = "<package>.<Type>.<Method>" — package extracted
//     from the file's `package` clause.
//
// Without this normalisation the unifier sees two different Method
// entities (one from gopls, one from tree-sitter) at the same
// source location, the touched-suffix lookup in change.process picks
// the wrong one, and validate-diff misses the expected
// flow_unreviewed finding.
func NewGoplsDriver() Driver {
	return newGenericDriver(driverConf{
		languageID:        "go",
		executable:        "gopls",
		args:              []string{"serve"},
		producedBy:        "extractor:lsp:gopls",
		qualify:           goQualify,
		postProcessSymbol: goPostProcessSymbol,
	})
}

// goQualify mirrors the §6.12 Method identity formula:
//
//	pkg.Type.Method  → qualified_name = "pkg.Type.Method", receiver = "pkg.Type"
//
// Used for nested DocumentSymbol shapes some gopls builds emit. The
// flat top-level shape is normalised by goPostProcessSymbol below.
func goQualify(parents []string, name string) string {
	if len(parents) == 0 {
		return name
	}
	cleaned := make([]string, len(parents))
	for i, p := range parents {
		p = strings.TrimPrefix(p, "(*")
		p = strings.TrimSuffix(p, ")")
		cleaned[i] = p
	}
	return strings.Join(append(cleaned, name), ".")
}

// goMethodNameRE matches gopls's flat Method designator:
//
//	(*Type).Method   ← pointer receiver
//	Type.Method      ← value receiver
//
// Capture groups: 1 = receiver type, 2 = method name.
var goMethodNameRE = regexp.MustCompile(`^\(?\*?([A-Z][A-Za-z0-9_]*)\)?\.([A-Za-z_][A-Za-z0-9_]*)$`)

// goPackageDeclRE matches a top-level `package X` declaration.
var goPackageDeclRE = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z_][A-Za-z0-9_]*)`)

// goPostProcessSymbol normalises gopls's flat Method shape and
// rewrites the qualified name with the file's package prefix so the
// §6.12 canonical key matches the tree-sitter side. Signature is
// also normalised: gopls returns `func(params) result` in Detail;
// tree-sitter emits `(params) result`. Stripping the `func` prefix
// keeps the per-language signature normaliser fed identical input
// from both sources, so MethodID(language, receiver, name, ns)
// hashes equal and the unifier merges into a single entity row.
func goPostProcessSymbol(sym *source_live.Symbol, fileBody []byte) {
	if sym == nil {
		return
	}
	pkg := goPackageName(fileBody)

	// Strip a leading `func ` / `func(` from gopls's signature
	// rendering so it matches what tree-sitter emits (`(params) ret`).
	if strings.HasPrefix(sym.Signature, "func(") {
		sym.Signature = strings.TrimPrefix(sym.Signature, "func")
	} else if strings.HasPrefix(sym.Signature, "func ") {
		sym.Signature = strings.TrimPrefix(sym.Signature, "func ")
	}

	if sym.Kind == source_live.SymbolKindMethod {
		if m := goMethodNameRE.FindStringSubmatch(sym.Name); m != nil {
			sym.Receiver = m[1]
			sym.Name = m[2]
		}
		if pkg != "" && sym.Receiver != "" {
			sym.QualifiedName = pkg + "." + sym.Receiver + "." + sym.Name
		}
		return
	}

	// For Functions / Classes / Interfaces, prepend the package name
	// so qualified names align with the tree-sitter parser
	// (`pkg.Func`, `pkg.Type`).
	if pkg != "" && sym.QualifiedName != "" && !strings.HasPrefix(sym.QualifiedName, pkg+".") {
		sym.QualifiedName = pkg + "." + sym.QualifiedName
	}
}

// goPackageName scans body for the first `package X` clause. Returns
// "" on missing / unreadable bodies; callers leave the qualified name
// unprefixed in that case.
func goPackageName(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if m := goPackageDeclRE.FindSubmatch(body); m != nil {
		return string(m[1])
	}
	return ""
}
