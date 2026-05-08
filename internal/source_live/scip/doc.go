// Package scip implements the SCIP-side fact source for source.live.
//
// SCIP (Source Code Intelligence Protocol — Sourcegraph's successor
// to LSIF) is a protobuf-based index format describing a codebase's
// symbol graph. v1 expects pre-existing `.scip-index/*.scip` files
// produced by language-specific indexers (scip-go, scip-typescript,
// scip-python). Auto-generation is deferred to P2's
// `graph-harness index` command.
//
// Layout:
//
//   - proto/         : vendored scip.proto schema + minimal hand-rolled
//     decoder (see proto/doc.go for rationale).
//   - reader.go      : Read(path) → *Index parser.
//   - import.go      : language-agnostic translator that emits
//     []source_live.Symbol from a *Document.
//   - import_go.go   : scip-go specific symbol-string parsing
//     (qualified-name reconstruction).
//   - import_ts.go   : scip-typescript specific.
//   - import_py.go   : scip-python specific.
//   - refresh.go     : fsnotify watcher + CLI-driven re-import; emits
//     source.live events with produced_by:
//     extractor:scip and source_class: index_scip.
//
// SPEC: §6.11 (SCIP role), §6.15 (pipeline summary).
package scip
