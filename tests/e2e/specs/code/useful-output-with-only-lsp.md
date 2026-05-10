# useful-output-with-only-lsp

> SPEC §6.11 build-order tolerance — code.core must produce useful output when **only** LSP is available (tree-sitter and SCIP disabled). Provenance carries a single `live_lsp` row.

**Wave:** code
**Tags:** code, single-source, lsp
**Spec:** `useful-output-with-only-lsp.yaml`

## Why this spec exists

Build-order tolerance (SPEC §6.11) is the contract that makes graph-harness usable on **any** machine, not just one with the full polyglot toolchain installed. Each source class — LSP / SCIP / tree-sitter — must independently produce useful output when it's the only one available. This spec is the **LSP-only variant**; sister specs cover SCIP-only and tree-sitter-only.

The risk being defended: an over-eager assumption deep in the unifier or selector path that "if no SCIP index exists, error" or "if tree-sitter didn't parse, fall back to nothing". The `--no-treesitter` and `--no-scip` flags were added (per-invocation toggle commit `a25f10c`) precisely so each source class is exercisable in isolation in e2e.

## Scenario

1. `go-module-with-checkout-validator` helper — Go module with `Validate`.
2. `graph-harness init` + install demo overlay.
3. `selectors test CheckoutValidator --no-treesitter --no-scip --json` with `GRAPH_HARNESS_ENABLE_LSP=1` — asserts `outcome: bound` + matched `CheckoutValidator`.
4. `code provenance "checkout.CheckoutValidator.Validate" --no-treesitter --no-scip --json` — asserts:
   - `source_count: 1`.
   - `source_class: live_lsp`.
5. **Negative-asserts** via `grep -c 'structural_treesitter\|index_scip' || true` — count must be `0`. No tree-sitter or SCIP rows leaked into provenance.

## Why three independent assertions

The first asserts resolution works; the second asserts provenance attribution is single-source-correct; the third asserts no other source class snuck in (the most likely failure mode is a stray tree-sitter row from an earlier index pass that wasn't cleared).

## What's covered vs deferred

- **Covered:** LSP-only path produces selector-resolvable, provenance-correct output.
- **Gating:** `feature:have-gopls` + `feature:p1-extractor-wiring`. The wiring gate matters because the `--no-*` flags ride alongside the production extractor wiring; before that landed, the flags were no-ops and the assertion was trivially true.
- **Compat:** `GRAPH_HARNESS_ENABLE_LSP=1` is the legacy env var, kept after the P1.L default-on flip (plan/phase-1-gaps.md §17 T50 + §13 spec-amendment notes) so this spec didn't need rewriting at the same time.

## Reference

- SPEC §6.11 — build-order tolerance contract
- `plan/phase-1-gaps.md` §13 — env var migration plan
- `internal/cli/extract_flags.go` — `--no-lsp` / `--no-scip` / `--no-treesitter` flags
- Sister specs: `useful-output-with-only-scip.yaml`, `useful-output-with-only-tree-sitter.yaml`
