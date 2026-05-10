# three-source-agreement-merges-provenance

> When LSP, SCIP, and tree-sitter all describe the same Go function, code.core materializes **one** entity with a **three-row** provenance — `live_lsp`, `index_scip`, `structural_treesitter` — and `source_count == 3`.

**Wave:** code
**Tags:** code, unification, p1t17, p1t19
**Spec:** `three-source-agreement-merges-provenance.yaml`

## Why this spec exists

This spec is **gate criterion 4** in plan §3 — "50-fixture three-source agreement". It's the single most important test for the multi-source design: it proves the unifier's fold (SPEC §6.11 §4.5) collapses three independent claims about the same symbol into a single entity *while preserving full per-source attribution*.

Two failure modes need to be impossible:

1. **Over-eager merge:** the entity exists but only one source's claim survives — provenance reports `source_count: 1`. The fold ate evidence.
2. **Under-eager merge:** three separate entities exist where there should be one. The unifier didn't recognize they describe the same symbol (FunctionID hash diverged across sources).

This spec defends against both with a Go function whose canonical signature LSP / SCIP / tree-sitter all agree on.

## Scenario

1. `go-module-with-checkout-validator` helper — clean Go module with `validator.go` defining `CheckoutValidator.Validate(cart any) error`.
2. `graph-harness init` + install demo overlay.
3. **Generate a real `.scip-index/`** by invoking `scip-go --output .scip-index/index.scip` directly. The orchestrator deliberately does not auto-generate indexes (P1.B v1 contract); the spec satisfies the requirement explicitly. This makes the spec **gated on real `scip-go` install** — `feature:have-scip-go` controls skip behavior on machines without it.
4. `graph-harness scip refresh` — synchronous import of the index.
5. `selectors test CheckoutValidator --json` with LSP enabled — asserts `outcome: bound`. (Sanity check before the real assertion.)
6. **The load-bearing assertion:** `code provenance "checkout.CheckoutValidator.Validate" --json` returns:
   - `source_count: 3` (the fold count).
   - Three per-source claims with `source_class`: `live_lsp`, `index_scip`, `structural_treesitter`.
   - `qualified_name: checkout.CheckoutValidator.Validate` — sentinel snake_case key, fails immediately on PascalCase JSON-tag drift.
7. Human rendering (`code provenance` without `--json`) — asserts `source_count:    3` plus the three source-class names appear in plain text.

## Why this is gated on `feature:p1-extractor-wiring`

The unifier only emits all three source classes when the production path (`indexWorkspaceCode`) actually pipes Symbols from every extractor into `Unifier.Unify`. Before the T-extractors wiring landed (commit `e31a8f7+`), the gate was off and this spec would silently false-pass on a single-source provenance row. The flag remains in the spec as a forward guard — flipping it off skips this spec rather than producing a misleading green.

## What's covered vs deferred

- **Covered:** three-source convergence + folded summary + per-source rows.
- **Not covered:** the divergence path (sister spec `three-source-disagreement-emits-symbol-disambiguation.yaml`).
- **Performance:** spec runs against one fixture, not 50. The 50-fixture variant is the **bench** wave (`bench/scenario-1-three-languages.yaml`); this spec is the surgical canary.

## Reference

- SPEC §6.11 §4.5 — three-source fold definition
- SPEC §6.12 — canonical key formula (must be stable across sources)
- `plan/phase-1-gaps.md` §1 P1.D — unification audit + gate-4 status
- `plan/phase-1-gaps.md` §10 gate 4 — pre/post P1.L status
- `internal/code_core/unify.go` — fold implementation
- `internal/code_core/provenance.go` — per-source SourceEntry + folded summary
- Sister: `three-source-disagreement-emits-symbol-disambiguation.yaml`
