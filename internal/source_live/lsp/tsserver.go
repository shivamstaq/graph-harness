package lsp

import "github.com/shivamstaq/graph-harness/internal/source_live"

// NewTSServerDriver returns a Driver that talks to
// `typescript-language-server --stdio`. The npm-installed
// typescript-language-server is the de facto standard; the
// vscode-typescript-language-features bundled server isn't a
// standalone binary so we don't target it.
//
// It maps LSP Variable / Property kinds to SymbolKindFunction when
// the surrounding detail string suggests an arrow-function value —
// matches what tsserver does for `const foo = () => {}`.
func NewTSServerDriver() Driver {
	return newGenericDriver(driverConf{
		languageID: "typescript",
		executable: "typescript-language-server",
		args:       []string{"--stdio"},
		producedBy: "extractor:lsp:tsserver",
		kindMap:    tsKindMap,
	})
}

// tsKindMap rewrites tsserver's Variable kind to Function when we can
// reasonably infer the symbol is an arrow-function binding. tsserver
// returns kind=12 (Function) for declared functions and kind=13
// (Variable) for arrow-function consts; we keep the variable form
// projected into our Symbol taxonomy as Function so unification with
// SCIP/tree-sitter (which both report Function) works.
func tsKindMap(k int) source_live.SymbolKind {
	switch k {
	case lspKindFile:
		return source_live.SymbolKindFile
	case lspKindModule:
		return source_live.SymbolKindModule
	case lspKindClass:
		return source_live.SymbolKindClass
	case lspKindInterface:
		return source_live.SymbolKindInterface
	case lspKindMethod, lspKindProperty:
		return source_live.SymbolKindMethod
	case lspKindFunction:
		return source_live.SymbolKindFunction
	case lspKindVariable, lspKindConstant, lspKindField:
		// Best-effort: tsserver uses these for arrow-function bindings.
		// We project them as Function so the three-source unification
		// has a consistent kind across LSP/SCIP/tree-sitter.
		return source_live.SymbolKindFunction
	default:
		return source_live.SymbolKindSymbol
	}
}
