# three-source-disagreement-emits-symbol-disambiguation

> When LSP and SCIP report a function with **different signatures** (because the SCIP index is stale relative to the live source), the unifier records both canonical IDs as separate entities **and** emits a `code.core.SymbolDisambiguation` event whose payload carries every source's claim.

**Wave:** code
**Tags:** code, unification, p1t18, p1t20
**Spec:** `three-source-disagreement-emits-symbol-disambiguation.yaml`

## Why this spec exists

This is **gate criterion 5** (plan §3) — the conflict-as-event contract from SPEC §6.12. The principle: **disagreement between sources is data, not an error**. Instead of picking a winner and silently discarding the loser, the unifier persists both candidate entities and emits a structured event whose `claims[]` array carries every source's view. Downstream — change.process, Studio's Conflicts panel, the TUI — all subscribe to the event and surface it as a `SymbolDisambiguation` finding.

The most common real-world cause is exactly the one the fixture reproduces: a checked-in `.scip-index/` that's stale relative to the live source tree. `gopls` sees the freshly edited signature; SCIP replays the old one. The wrong design — picking gopls and dropping SCIP — would erase the historical claim that motivates the disambiguation finding to begin with.

## Scenario

1. `go-module-with-checkout-validator` helper — Go module with `Validate(cart any) error`.
2. `graph-harness init` + install demo overlay.
3. **Stage the SCIP claim before the divergence**: `scip-go --output .scip-index/index.scip` captures the original signature.
4. **Drift the live source**: `sed -i.bak 's/Validate(cart any)/Validate(cart any, ctx string)/' validator.go`. Now `gopls` sees a 2-arg signature; SCIP still replays the 1-arg version.
5. `graph-harness scip refresh` against the now-stale index.
6. Assertions:
   - `selectors test CheckoutValidator --json` — `outcome: bound` (resolution still finds at least one match; the resolver picks via the multi-anchor ladder. The spec doesn't pin which one.).
   - **The load-bearing assertion:** `code events --kind SymbolDisambiguation --json` returns an event with:
     - `kind: SymbolDisambiguation`, `layer: code.core`.
     - A `claims[]` array carrying each source's `source_class` — at minimum `live_lsp` and `index_scip` (gate 5 requires every source's claim to be carried).
   - `code provenance "checkout.CheckoutValidator.Validate" --json` — confirms the qualified_name resolves (one of the two canonical IDs is returned; the disambiguation event carries both in its claims[]).

## What's covered vs deferred

- **Covered:** divergence detection between LSP and SCIP, event emission with full claims, both canonical IDs persisted.
- **Not exercised here:** consumer-side rendering of the event (TUI Conflicts panel, change.process `coverage_warning`). Those are tested in their own surfaces.
- **Gating:** `feature:have-scip-go` (live `scip-go` required) + `feature:p1-extractor-wiring` (the unifier only emits this event when at least two real sources produce divergent canonical IDs at the same source-text location).

## Reference

- SPEC §6.12 — conflict-as-event contract
- `plan/phase-1-gaps.md` §10 gate 5 — gate criterion this spec asserts
- `plan/phase-1-gaps.md` §1 P1.G — change.process `unresolved_anchor` + `SymbolDisambiguation` wiring
- `internal/code_core/unify.go` — divergence detection
- `internal/code_core/event_*.go` — event emission
- Sister: `three-source-agreement-merges-provenance.yaml` (the convergence path)
