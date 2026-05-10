# useful-output-with-only-tree-sitter

> SPEC §6.11 build-order tolerance — code.core must produce useful output when **only** tree-sitter is available (LSP and SCIP disabled). Provenance carries a single `structural_treesitter` row.

**Wave:** code
**Tags:** code, single-source, tree-sitter
**Spec:** `useful-output-with-only-tree-sitter.yaml`

## Why this spec exists

This is the **lower bound** of the build-order tolerance triplet. tree-sitter is the always-on source — cgo-bundled, no install required, no `.scip-index/` needed. Whatever this spec guarantees is the floor of what graph-harness can do on a fresh machine with zero language-toolchain setup.

In practical terms: a developer cloning the repo and running `graph-harness selectors test` on Day 0 hits exactly this code path. If it doesn't produce useful output, the user concludes "this tool is broken" and never gets to the LSP/SCIP layers.

## Scenario

1. `go-module-with-checkout-validator` helper — Go module.
2. `graph-harness init` + install demo overlay.
3. `selectors test CheckoutValidator --no-lsp --no-scip --json` — no LSP env var. Asserts `outcome: bound` + `CheckoutValidator` match.
4. `code provenance "checkout.CheckoutValidator.Validate" --no-lsp --no-scip --json` — asserts `source_count: 1` + `source_class: structural_treesitter`.
5. Negative-assert: `grep -c 'live_lsp\|index_scip'` returns `0`.

## What's covered vs deferred

- **Covered:** tree-sitter-only resolution + single-source provenance.
- **Gating:** `feature:p1-extractor-wiring` only — no LSP/SCIP feature gate because tree-sitter is always available. The wiring gate matters because pre-wiring the `--no-*` flags were no-ops and "only ts row in provenance" was trivially true.
- **Not exercised:** TS / Python tree-sitter walkers. The Go walker is the canary; sister test `extract/tree-sitter/tree-sitter-handles-syntax-errors-gracefully.yaml` covers the malformed-input degradation path.

## Reference

- SPEC §6.11 — build-order tolerance
- `plan/phase-1-gaps.md` §1 P1.C — tree-sitter audit
- `internal/source_live/treesitter/parser_*.go` — manual AST walkers
- `internal/cli/extract_flags.go` — `--no-*` toggles
- Sister specs: `useful-output-with-only-lsp.yaml`, `useful-output-with-only-scip.yaml`
