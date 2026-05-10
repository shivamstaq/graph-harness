# explain-anchor-ladder

> `graph-harness selectors test --explain` prints the per-anchor evaluation trace: which anchor matched, the score for each rung, which lower-priority anchors weren't consulted because an earlier one satisfied its threshold.

**Wave:** selectors
**Tags:** selectors, p1t29, gate-criterion-8
**Spec:** `explain-anchor-ladder.yaml`

## Why this spec exists

This is **gate criterion 8** (plan §3) — the `--explain` ladder. The resolver's anchor-priority logic is the most complex piece of code.core; users debugging a selector that "should have bound but didn't" need a transparent rendering of *why* each rung was tried and *what score* each produced. Without `--explain`, the resolver is a black box.

Two distinct rendering paths matter:

1. **Text trace** (default `--explain`) — human-friendly, "outcome: bound", "not consulted", "fallback scored 0.85".
2. **JSON trace** (`--explain --json`) — machine-readable, with `kind`, `outcome_when_taken`, `skipped` per rung.

Both have to round-trip the same logical structure or downstream consumers (Studio's Explain panel, agent introspection via MCP) drift from the CLI.

The spec exercises **two scenarios** to cover the two outcome shapes:

- **Primary wins** → `outcome: bound`, lower rungs are `skipped: true` / `not consulted`.
- **Primary misses, fallback wins** → `outcome: reanchored`, primary marked `no candidates`, fallback scores `0.85`, body_hash `not consulted`.

## Scenario

1. `go-module-with-checkout-validator` helper.
2. `graph-harness init`.
3. **Two-selector overlay**:
   - `CheckoutValidatorPrimary` — correct `qualified_name`, signature, and a deliberately-non-matching `body_hash`. Should bind on the primary; sig/body never consulted.
   - `CheckoutValidatorStale` — wrong `qualified_name`, but matching signature, plus the deliberately-non-matching body_hash. Should fall through to `function_signature` → reanchored at confidence 0.85.
4. Four assertions:
   - `selectors test CheckoutValidatorPrimary --explain` (text) — `anchor ladder`, `outcome: bound`, `not consulted`.
   - `selectors test CheckoutValidatorPrimary --explain --json` — `trace[]` carries `kind: qualified_name`, `outcome_when_taken: bound`, `skipped: true` (for the unconsulted rungs).
   - `selectors test CheckoutValidatorStale --explain` (text) — `outcome: reanchored`, `no candidates`, `fallback scored 0.85`, `not consulted`.
   - `selectors test CheckoutValidatorStale --explain --json` — `outcome: reanchored`, `via_anchor: function_signature`, all three anchor kinds present in trace with the expected dispositions.

## Why `body_hash "sha256:never-matches"` matters

The deliberately-non-matching body_hash is the **third rung**. Both selectors need a third rung the primary/secondary outcomes don't reach, so the spec can verify the "not consulted" rendering for both the primary-wins and fallback-wins cases. Without it, the trace would only show two rungs and the "skipped lower-priority" assertion has no anchor to bind to.

## What's covered vs deferred

- **Covered:** text + JSON trace for both bound and reanchored outcomes; "not consulted" rendering; default function_signature score (0.85).
- **Not asserted:** specific score formulas. The 0.85 number is a default, not a contract — it lives in `internal/code_core/resolver/scores.go` and changes with anchor improvements. If the test starts asserting on specific numeric scores beyond the default, that becomes a versioned-contract concern.

## Reference

- SPEC §3.2 — selector resolution outcomes (`bound | reanchored | unresolved`)
- `plan/phase-1-gaps.md` §1 P1.F — resolver audit, including the `--explain` ladder
- `plan/phase-1-gaps.md` §10 — gate criterion 8 status
- `internal/code_core/resolver/explain.go` — trace rendering
- Sister: `validate-diff/reanchor-on-rename.yaml` (uses the same `--explain` ladder under a real rename)
