# change-process-finding-kinds-shape

> change.process emits findings in the SPEC §8.2 `ValidationFinding` shape — `kind`, `severity`, `subject`, `evidence`, `repair` — and `flow_unreviewed` still fires under the new pipeline wiring.

**Wave:** validate-diff
**Tags:** validate-diff, change-process, p1t30, p1t31
**Spec:** `finding-kinds-shape.yaml`

## Why this spec exists

This is the **shape contract** for `validate-diff` output (P1.T30, T31). SPEC §8.2 pins the `ValidationFinding` envelope. Five fields have to be correct for any consumer (Studio's Findings panel, agent introspection, CI gating scripts) to reason about a finding:

- `kind`: discriminator string (`flow_unreviewed`, `selector_reanchored`, `symbol_disambiguation`, `unresolved_anchor`, etc.)
- `severity`: `info | low | medium | high | critical`
- `subject`: the entity / location the finding pertains to
- `evidence`: structured supporting data (varies by kind)
- `repair`: structured suggestion (empty `{}` until P3 lands the rule engine)

This spec uses the existing tracer-validate-diff fixture (no diff reaches symbol_disambiguation or unresolved_anchor in the happy path; those are exercised by the three-source disagreement spec and `reanchor-on-rename.yaml`). Its job is the **shape-baseline guard**: that `flow_unreviewed` still fires under the post-P1.G pipeline wiring with the well-formed envelope.

`"repair":{}` is intentional. The full-shape repair payload lives in P3 (rule engine, see `plan/03-rule-engine-and-repairs.md`). Until then the field is required-but-empty; specs in the P3 wave will replace empty-payload assertions.

## Scenario

1. `go-module-with-checkout-validator` helper — Go module + `demo.diff` (touches `Validate` without acknowledging the flow).
2. `graph-harness init` + install demo overlay.
3. `validate-diff --diff demo.diff --json` — asserts:
   - `kind: flow_unreviewed`.
   - `severity: medium` (the default for unacknowledged-flow findings).
   - `evidence` field present.
   - `repair: {}` (empty payload — P3 placeholder).

## What's covered vs deferred

- **Covered:** envelope shape for the canonical `flow_unreviewed` kind.
- **Not exercised here:** other finding kinds. `validate-diff/reanchor-on-rename.yaml` covers `selector_reanchored`. `code/three-source-disagreement-emits-symbol-disambiguation.yaml` covers `SymbolDisambiguation` events. `unresolved_anchor` is exercised in P1.G unit tests; e2e coverage lands once a fixture reliably produces it.
- **Deferred (P3):** non-empty `repair` shape.

## Reference

- SPEC §8.1 — twelve-stage pipeline
- SPEC §8.2 — `ValidationFinding` shape
- `plan/phase-1-gaps.md` §1 P1.G — change.process wiring
- `internal/change_process/` — pipeline implementation
- Sister: `validate-diff/reanchor-on-rename.yaml`
