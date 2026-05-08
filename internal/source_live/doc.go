// Package source_live implements the source.live layer: the raw extractor
// fact stream that feeds code.core. v1 unifies three independent fact
// sources — LSP (live), SCIP (indexed), tree-sitter (structural) — via
// content-addressable keys and conflict-as-event semantics (SPEC §6.11).
//
// Per-language tree-sitter parsers (Go, TypeScript, Python) extract
// structural facts from source files; the multi-language Watcher
// (watcher.go) routes fsnotify file events to the right parser by file
// extension and emits FileChanged / FileParsed FileEvents into the
// layer. LSP drivers live under lsp/; SCIP wire-format readers and
// per-language importers live under scip/.
//
// The Symbol envelope (symbol.go) is the published cross-source
// contract — every extractor emits []Symbol with identical semantics
// so that code.core can compute the same canonical content-addressable
// key from each source independently and unify them via the three-source
// rule (SPEC §6.12).
//
// SPEC: §2.3 (layer purpose + freshness states), §6.11 (three-input
// model), §6.12 (identity model), §6.15 (code-fact pipeline summary),
// §6.17 (approved cgo dependencies — tree-sitter is the only cgo
// boundary; LSP and SCIP are pure Go subprocess + protobuf).
package source_live
