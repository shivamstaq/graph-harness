// Package proto carries the vendored Sourcegraph SCIP protobuf schema
// (scip.proto) plus a minimal hand-rolled wire-format decoder for the
// subset of fields source.live actually consumes.
//
// Why hand-rolled, not protoc-generated?
//
//   - The §6.17 evidence-audit pinning rule asks for a vendored proto
//     schema, not a transitive dependency on github.com/scip-code/scip
//     (which has been renamed from github.com/sourcegraph/scip — see
//     coordinator note).
//   - The subset we need is small (Index → Document → SymbolInformation
//     / Occurrence; ~10 fields total), well below the protoc threshold
//     where generated code pays for itself.
//   - Avoiding `protoc` in CI keeps the build matrix tiny.
//
// Wire format follows the Protobuf Encoding spec verbatim: we use the
// google.golang.org/protobuf/encoding/protowire helpers (already a
// transitive dep) for varint / length-prefix handling. The vendored
// scip.proto itself is the canonical schema reference; if the upstream
// schema evolves, update both that file and the matching field-number
// constants here as a paired change.
//
// SPEC: §6.11 (SCIP role), §6.15 (pipeline), §6.17 (vendoring).
package proto
