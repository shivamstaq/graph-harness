# lsp-extracts-ts-functions

> When `typescript-language-server` is on PATH, source.live's LSP host extracts TS symbols and code.core stores entities with `extractor:lsp:tsserver` provenance.

**Wave:** extract
**Tags:** extract, lsp, p1t03
**Spec:** `lsp-extracts-ts-functions.yaml`

## Why this spec exists

Per-language LSP smoke for TypeScript (P1.T03), structurally identical to `lsp-extracts-go-functions.yaml`. The TS variant matters because:

1. **Module resolution differs** — TS's `qualified_name` rule uses the basename (`validator.ts` → `validator.<class>.<method>`), not the package-qualified form Go uses. The spec hard-codes `validator.CheckoutValidator.validate` to catch any drift in the parser-emitted module path convention.
2. **`tsserver` is the canonical id** even though the binary is `typescript-language-server` — `produced_by: extractor:lsp:tsserver` decouples the canonical identifier from the actual binary name, which matters for the `[lsp.typescript] server = "vtsls"` override path.

## Scenario

1. `ts-module-with-checkout-validator` helper — TS package with `validator.ts` defining `class CheckoutValidator` + `validate(cart): boolean`.
2. `graph-harness init` + install `demo.checkout.gh` overlay.
3. `graph-harness selectors test CheckoutValidator --json` with LSP enabled — asserts:
   - `outcome: bound`.
   - `qualified_name: validator.CheckoutValidator.validate` (basename module rule).
   - `via_anchor: qualified_name`.
4. `graph-harness code provenance "CheckoutValidator" --language typescript --json` — provenance on the class type (LSP-only, tree-sitter doesn't emit it as a top-level entity). Asserts `source_class: live_lsp` + `produced_by: extractor:lsp:tsserver`.

## What's covered vs deferred

- **Covered:** tsserver lifecycle, basename qualified_name rule, canonical-id decoupling.
- **Not exercised here:** `[lsp.typescript] server = "vtsls"` override. The registry has the entry (`internal/source_live/detect/config.go::KnownLSPServers`) but full e2e validation lands in P2.
- **Not exercised:** `deno.json` workspaces (canonical server is `deno lsp` — separate language id). Plan/phase-1-gaps.md §8.2 special case; P2.

## Reference

- `plan/phase-1-gaps.md` §1 P1.A — multi-LSP host audit
- SPEC §6.11
- `internal/source_live/lsp/registry.go`
- `internal/extract/orchestrator.go::gatherLSP`
- Sister specs: `lsp-extracts-go-functions.yaml`, `lsp-extracts-python-functions.yaml`
