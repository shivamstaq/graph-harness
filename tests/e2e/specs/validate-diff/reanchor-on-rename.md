# reanchor-on-rename

> A function rename → the selector with `qualified_name` + `function_signature` anchors **re-binds via the fingerprint fallback** at confidence ≥ 0.75. `validate-diff` emits a `selector_reanchored` finding identifying the fingerprint that paid for the rebind.

**Wave:** validate-diff
**Tags:** validate-diff, change-process, gate-criterion-6, reanchor
**Spec:** `reanchor-on-rename.yaml`

## Why this spec exists

This is **gate criterion 6** (plan §3) — the rename re-anchor flow. The contract: when a developer renames a function, selectors targeting the old name must **automatically re-bind to the new name** via a structural anchor (function_signature, body_hash, etc.) without manual intervention. This is the killer feature that justifies multi-anchor selectors over plain symbol references — an editor refactor doesn't break every flow that touched the renamed symbol.

The threshold contract: re-anchor requires confidence **≥ 0.75**. function_signature's default score is 0.85, well above the bar. `validate-diff` then emits a `selector_reanchored` finding so the user (or CI) can see what just happened — the rebind isn't silent.

Plan §3 calls out TypeScript as the example, but the resolver + pipeline are language-agnostic and Go gives the most reliable e2e environment (LSP/SCIP/tree-sitter all available without extra toolchains, no venv-resolution concern).

## Scenario

The tricky part of this spec is **fixture ordering**. The workspace must be initialized AFTER the rename so the indexer's first pass sees only the renamed function. If init runs first, code.core would observe `Validate` (the old name); after the rename it would observe `ValidateV2` (the new name); the unifier would carry both as separate entities; and the test would muddle "rename detection" with "stale-entity garbage collection" (which is a later-phase concern). So:

1. `go-module-with-checkout-validator` helper — clean Go module with `Validate(cart any) error`.
2. **Stage the rename** by writing `rename.diff` and applying it via `patch -p1` — the source now defines `ValidateV2`. The diff itself is preserved as `rename.diff` so step 3's `validate-diff` has a unified-diff input.
3. `graph-harness init` — first-pass indexer sees only `ValidateV2`.
4. **Install overlay pointing at the OLD name** — `qualified_name "checkout.CheckoutValidator.Validate"` plus `function_signature sig(any) -> error` as the structural fallback. The qualified_name is now stale.
5. Three assertions:
   - `selectors test CheckoutValidator --explain` — `outcome: reanchored`, `via_anchor: function_signature`, `confidence: 0.85`. Trace: `no candidates` for the qualified_name miss, `fallback scored 0.85` for the win.
   - `validate-diff --diff rename.diff --json` — finding `kind: selector_reanchored`, payload contains `outcome=reanchored`, `anchor=function_signature`, `confidence=0.85` (gate criterion 6 threshold).
   - `selectors test CheckoutValidator --json --explain` — JSON trace mirrors the text outcome (`outcome: reanchored`, `via_anchor: function_signature`, full ladder rendered).

## What's covered vs deferred

- **Covered:** the full reanchor flow on a Go fixture: rename → resolver fallback → finding emission.
- **Not exercised here:** rename detection in TS / Python (the resolver code paths are shared; per-language verification lands when Tier-3 specs for those languages stabilize).
- **Deferred:** stale-entity garbage collection. After the rename, the workspace contains only `ValidateV2`; if the indexer had observed `Validate` pre-rename and not garbage-collected, the resolver would prefer the still-present-but-pre-rename entity. P1 dodges this by initializing post-rename; entity GC on rename is an explicit later-phase concern.

## Reference

- SPEC §3.2 — selector resolution outcomes
- `plan/phase-1-gaps.md` §1 P1.G — change.process wiring
- `plan/phase-1-gaps.md` §10 gate 6 — gate criterion this spec asserts
- `internal/code_core/resolver/scores.go` — function_signature default score (0.85)
- Sister: `selectors/explain-anchor-ladder.yaml` (the trace contract reanchor produces)
