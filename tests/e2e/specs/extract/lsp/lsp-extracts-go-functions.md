# lsp-extracts-go-functions

> When `gopls` is on PATH, source.live's LSP host extracts Go symbols and code.core stores entities with `extractor:lsp:gopls` provenance.

**Wave:** extract
**Tags:** extract, lsp, p1t02
**Spec:** `lsp-extracts-go-functions.yaml`

## Why this spec exists

This is the **per-language LSP smoke** for Go (P1.T02). Three things must hold simultaneously for a single Go entity to be useful in code.core:

1. The LSP host successfully spawns `gopls` and survives initialize → `textDocument/documentSymbol`.
2. The orchestrator (`internal/extract/orchestrator.go::gatherLSP`) translates `gopls`'s SymbolInformation into `source_live.Symbol` envelopes.
3. The unifier writes those into a code.core entity carrying a `live_lsp` provenance row whose `produced_by` field is exactly `extractor:lsp:gopls`.

If any of those three steps regresses, this spec fails. Sister specs `lsp-extracts-ts-functions.yaml` and `lsp-extracts-python-functions.yaml` mirror the template per language so a Go-specific bug doesn't masquerade as a generic LSP failure.

## Scenario

1. `go-module-with-checkout-validator` helper — Go module with `internal/checkout/validator.go` defining `CheckoutValidator.Validate(cart any) error` plus the demo `.gh` overlay.
2. `graph-harness init` + copy `demo.checkout.gh` into `.graph-harness/overlay/checkout.gh`.
3. `graph-harness selectors test CheckoutValidator --json` with `GRAPH_HARNESS_ENABLE_LSP=1` — strong probe asserting:
   - `outcome: bound`.
   - `qualified_name: checkout.CheckoutValidator.Validate` (matches the source).
   - `via_anchor: qualified_name` (resolved through the primary anchor, not a fallback).
4. `graph-harness code provenance "checkout.CheckoutValidator" --json` — provenance probe on the **type** (not the method): `gopls`'s DocumentSymbol returns Class symbols for type declarations, but tree-sitter doesn't, so this entity's source list is *only* `live_lsp` — clean canary for the producer attribution. Asserts `source_class: live_lsp` and `produced_by: extractor:lsp:gopls`.

## Why the type, not the function, for provenance

The function-level test would have to navigate the comment in `internal/extract/orchestrator.go::filterOutFunctions` (signature alignment between LSP DocumentSymbol responses and tree-sitter callable nodes). Asserting on the Class entity sidesteps that — a clean LSP-only attribution for an entity tree-sitter never produces.

## What's covered vs deferred

- **Covered:** `gopls` lifecycle, DocumentSymbol → entity flow, producer attribution.
- **Not exercised:** `Definition` / `References` LSP methods. Per plan/phase-1-gaps.md §4.2, those interface methods exist on the driver but are dead code in the orchestrator — explicitly deferred to P2 (P2 framework extractors need call graphs).
- **Gating:** `feature:have-gopls` (host-installed gopls) + `feature:p1-multi-language`. `GRAPH_HARNESS_ENABLE_LSP=1` is the legacy compat shim — kept after the P1.L default-on flip (plan/phase-1-gaps.md §17 T50) so existing specs don't churn.

## Reference

- `plan/phase-1-gaps.md` §1 P1.A — multi-LSP host audit
- SPEC §6.11 — `live` source = primary
- `internal/source_live/lsp/registry.go` — `exec.LookPath` graceful-degrade
- `internal/extract/orchestrator.go::gatherLSP` — DocumentSymbol → Symbol translation
- `internal/code_core/unify.go` — entity merge + producer attribution
- Sister specs: `lsp-extracts-ts-functions.yaml`, `lsp-extracts-python-functions.yaml`
