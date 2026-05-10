# scip-import-replays-events

> SCIP wire-format import lands code.core entities + provenance from a static `.scip` blob — no `scip-go` install required.

**Wave:** extract
**Tags:** extract, scip, p1t10
**Spec:** `scip-import-replays-events.yaml`

## Why this spec exists

SCIP is the **batch source** in the three-source model (SPEC §6.11). It runs offline against an `.scip-index/` directory the user generates separately, then the orchestrator replays its `SymbolInformation` + `Occurrence` rows on every workspace open.

Two things have to work for SCIP to be useful, and **neither requires the `scip-go` binary to be installed locally**:

1. The hand-rolled SCIP wire-format reader (`internal/source_live/scip/proto/decode.go`) parses the protowire stream correctly. SPEC §6.17 explicitly committed to "Build-our-own — SCIP wire-format reader" rather than depending on `google.golang.org/protobuf` for runtime decoding.
2. The Go importer (`internal/source_live/scip/import_go.go`) translates the decoded rows into `source_live.Symbol` envelopes with the right `produced_by: extractor:scip:scip-go` attribution.

This spec uses a **canonical fixture blob** committed at `tests/testdata/scip/sample.scip`. That blob was generated *once* against a controlled source tree (`pkg/checkout/validator.go` containing `Validator`, `Validator.Validate`, `Helper`) and serves as a wire-format canary: if upstream `scip-go` ever changes its emission, this spec catches it before users do.

## Scenario

1. `go-module-with-checkout-validator-and-scip-index` helper — stages a Go module **and** copies `tests/testdata/scip/sample.scip` to `.scip-index/checkout.scip`. The fixture's document path (`pkg/checkout/validator.go`) deliberately differs from the helper's source tree (`internal/checkout/`) so SCIP-imported entities don't collide with tree-sitter ones — that keeps source attribution unambiguous.
2. `graph-harness init` + overlay install.
3. **SCIP-only** assertions, all with `--no-treesitter --no-lsp`:
   - `code provenance "pkg/checkout.Validator.Validate"` — asserts `qualified_name`, `language_id: go`, `source_class: index_scip`, `produced_by: extractor:scip:scip-go`.
   - `code provenance "pkg/checkout.Helper"` — second symbol from the fixture; asserts `source_class: index_scip`.
   - `code list` — asserts the `Validator` *type* symbol also lands (the importer maps SCIP's `Struct` kind to our `SymbolKindClass`).

## What's covered vs deferred

- **Covered:** wire-format decode, SCIP→Symbol→entity flow, three SCIP symbol kinds (method, function, type).
- **Covered (via fixture):** the entire SCIP path runs without `scip-go` installed (plan/phase-1-gaps.md §1 Barrier 3 workaround).
- **Not exercised:** SCIP-TS / SCIP-Python importers (`internal/source_live/scip/import_{ts,py}.go`). They share the wire decoder; per-language importers diverge in symbol-kind mapping and are tested via unit tests + the bench scenario.
- **Not exercised:** SCIP refresh on file change (`internal/source_live/scip/refresher.go`). The refresher is exercised by `code/three-source-{agreement,disagreement}-*.yaml` (which generate live SCIP indexes via `scip-go` then call `scip refresh`).
- **Tier-2 hermetic:** runs without external installs. Tier-3 with real `scip-go` index regeneration is the canonical verification the wire format hasn't drifted upstream.

## Reference

- `plan/phase-1-gaps.md` §1 P1.B — SCIP audit and embedded-fixture decision
- `plan/phase-1-gaps.md` §1 Barrier 3 — install-recipe context
- SPEC §6.17 — "Build-our-own — SCIP wire-format reader"
- `internal/source_live/scip/proto/decode.go` — hand-rolled protowire decoder
- `internal/source_live/scip/import_go.go` — Go importer (Struct → Class mapping)
- `tests/testdata/scip/sample.scip` + `tests/testdata/scip/README.md` — fixture & generation notes
