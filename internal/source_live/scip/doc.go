// Package scip implements the SCIP-side fact source for source.live.
//
// SCIP (Source Code Intelligence Protocol — Sourcegraph's successor
// to LSIF) is a protobuf-based index format describing a codebase's
// symbol graph. v1 expects pre-existing `.scip-index/*.scip` files
// produced by language-specific indexers (scip-go, scip-typescript,
// scip-python). Auto-generation is deferred to P2's
// `graph-harness index` command.
//
// # Why a hand-rolled wire reader (SPEC §6.17 "Build-our-own")
//
// The upstream Sourcegraph SCIP Go module was renamed mid-2025 from
// `github.com/sourcegraph/scip` to `github.com/scip-code/scip` (the
// original org transferred ownership). Per SPEC §6.17's bus-factor /
// staleness criterion — the same one used to reject CozoDB — we don't
// take the renamed module as a critical-path production dependency.
// Instead the wire reader is hand-rolled with a bounded scope:
// Index → Document → SymbolInformation/Occurrence with ~10 fields
// total. Decoding uses google.golang.org/protobuf/encoding/protowire
// (already a transitive dep), so no `protoc` build-step enters CI.
//
// The canonical `scip.proto` schema ships verbatim under
// proto/scip.proto so future maintainers (or a swap-in to a real
// upstream module later) can compare against the authoritative
// schema. A wire-format property test under
// internal/source_live/scip/wire_property_test.go reads a static
// `.scip` blob produced by the upstream encoder, decodes it through
// our reader, and asserts every consumed field round-trips — the
// canary against silent wire-format drift.
//
// # Regenerating the wire-format fixture
//
// The fixture at tests/testdata/scip/sample.scip was produced by the
// upstream `github.com/scip-code/scip/bindings/go/scip` Go encoder
// via the build-tag-gated tool at cmd/genfixture/. The tool lives in
// its own Go module so the renamed dependency stays out of the main
// go.mod. Regenerate via:
//
//	cd internal/source_live/scip/cmd/genfixture
//	go run -tags genscipfixture .
//
// To bump the encoder pin first:
//
//	cd internal/source_live/scip/cmd/genfixture
//	go get github.com/scip-code/scip/bindings/go/scip@<new-version>
//	go mod tidy
//
// See tests/testdata/scip/README.md for the encoder lockfile.
//
// # Layout
//
//   - proto/         : vendored scip.proto schema + minimal hand-rolled
//     decoder (see proto/doc.go).
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
//   - cmd/genfixture: build-tag-gated tool (own go.mod) that
//     regenerates the wire-format property-test fixture using the
//     upstream encoder. Not built in normal `go build ./...` runs.
//
// SPEC: §6.11 (SCIP role), §6.15 (pipeline summary), §6.17
// (Build-our-own — the SCIP wire-format reader entry).
package scip
