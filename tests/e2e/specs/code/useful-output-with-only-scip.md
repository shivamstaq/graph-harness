# useful-output-with-only-scip

> SPEC §6.11 build-order tolerance — code.core must produce useful output when **only** SCIP is available (tree-sitter and LSP disabled). Provenance carries a single `index_scip` row.

**Wave:** code
**Tags:** code, single-source, scip
**Spec:** `useful-output-with-only-scip.yaml`

## Why this spec exists

The SCIP-only variant of the build-order tolerance triplet. Of the three sources, SCIP is the most operationally distinct: it's the only one that requires explicit user action (`scip-go --output ...`) to populate. Asserting that the SCIP-only path produces useful output exercises a specific concern: **the unifier must accept SCIP claims without LSP/tree-sitter cross-validation** and still emit a coherent entity.

This is the easiest to break with a careless invariant — "every entity has at least one tree-sitter claim" — so the spec is structured to make a regression of that flavor fail loudly.

## Scenario

1. `go-module-with-checkout-validator` helper.
2. `graph-harness init` + install demo overlay.
3. **Generate the SCIP index** explicitly via `scip-go --output .scip-index/index.scip` (orchestrator does not auto-generate per P1.B).
4. `graph-harness scip refresh` — synchronous import.
5. `selectors test CheckoutValidator --no-treesitter --no-lsp --json` — asserts `outcome: bound` + matched `CheckoutValidator`. **No `GRAPH_HARNESS_ENABLE_LSP`** — LSP off, SCIP-only.
6. `code provenance "checkout.CheckoutValidator.Validate" --no-treesitter --no-lsp --json` — asserts `source_count: 1` + `source_class: index_scip`.
7. Negative-assert: `grep -c 'structural_treesitter\|live_lsp'` returns `0`.

## What's covered vs deferred

- **Covered:** SCIP-only resolution + single-source provenance.
- **Gating:** `feature:have-scip-go` (real `scip-go` required to generate the index) + `feature:p1-extractor-wiring`.
- **Not exercised:** SCIP-TS / SCIP-Python single-source paths. The wire decoder is shared; per-language importer differences are covered by unit tests + the bench scenario.

## Reference

- SPEC §6.11 — build-order tolerance
- `plan/phase-1-gaps.md` §1 P1.B — SCIP audit
- `internal/source_live/scip/refresher.go` — refresh path
- `internal/cli/extract_flags.go` — `--no-*` toggles
- Sister specs: `useful-output-with-only-lsp.yaml`, `useful-output-with-only-tree-sitter.yaml`
