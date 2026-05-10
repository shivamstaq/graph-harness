# review-queue-needs-evidence-state

> review.queue auto-routes proposals missing required evidence to the `needs_evidence` state per SPEC §10.4. Submitting with full evidence lands `pending_review` directly.

**Wave:** review
**Tags:** review, review-queue, p1t32, p1t33
**Spec:** `needs-evidence-state.yaml`

## Why this spec exists

review.queue is the **human-loop** layer: agents and humans both submit proposals to change `semantic.overlay` (new invariants, anchor updates, flow definitions). Per SPEC §10.4, every proposal kind has a manifest specifying the required evidence to enter the queue. A proposal missing that evidence shouldn't sit in `pending_review` for a human to discover — it should auto-route to `needs_evidence` with a structured "missing:" list pointing at exactly what's required.

The core contracts the spec defends:

1. **Evidence-driven routing**: missing evidence → `needs_evidence`, full evidence → `pending_review`.
2. **Per-kind manifests**: an `invariant_proposal` requires symbol + test references; an `anchor_update` requires a `drift_signal` even when the candidate match is high-confidence.
3. **`anchor_update` strictness** is non-obvious: a 0.9-confidence candidate match is NOT enough on its own — without a drift signal there's no observed reason for the anchor to need updating. The state stays `needs_evidence`.

If routing collapses to "always needs review" or "always pending", the queue is useless and humans either ignore everything or get drowned.

## Scenario

1. `go-module-empty` helper — minimal repo (review.queue doesn't need source for this spec).
2. `graph-harness init`.
3. Three submissions:
   - **Invariant without evidence** → `state: needs_evidence`, output mentions `missing:` (the per-manifest list of what's required).
   - **Invariant with full evidence** (symbol + test) → `state: pending_review`. Routes directly to human review.
   - **Anchor update with high-confidence candidate but no drift signal** → `state: needs_evidence`, output mentions `drift_signal`. Confirms manifest strictness.

## What's covered vs deferred

- **Covered:** routing logic for `invariant_proposal` and `anchor_update` kinds.
- **Not exercised here:** the inverse (`add-evidence` → promote to `pending_review`). The plan §1 P1.H closure mentions it; the spec was scoped to routing-on-submit because that's the gate-blocker.
- **Deferred:** other proposal kinds (`flow_definition`, `selector_revision`) have manifests but P1 focuses on the two highest-frequency kinds. Coverage extends in P4 as more kinds graduate.

## Reference

- SPEC §10.4 — review.queue states + manifests
- `plan/phase-1-gaps.md` §1 P1.H — review.queue audit
- `internal/review_queue/` — implementation
- `internal/review_queue/manifests/` — per-kind evidence requirements
