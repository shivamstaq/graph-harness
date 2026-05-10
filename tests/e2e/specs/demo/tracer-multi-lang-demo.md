# tracer-multi-lang-demo

> Phase-1 capstone — same `.gh` flow targets equivalent functions in Go / TS / Python repos; the multi-language pipeline runs end-to-end.

**Wave:** demo
**Tags:** demo, capstone, p1t41
**Spec:** `tracer-multi-lang-demo.yaml`

## Why this spec is the wave gate

Phase 1's discipline mirrors phase 0's: "stable, demonstrable end-to-end product with every component touched in some form." If this spec passes, the multi-language story holds together. If it regresses, the spine is broken.

The smoke test under `tests/smoke/p1/demo.sh` is a thin wrapper that mirrors the same scenario plus oracle scoring (it walks the bench `scenario1/{go,ts,py}` fixtures explicitly), so `just smoke` and the gotit capstone stay aligned. The spec itself is the gotit-wave version: it runs against a polyglot fixture, exercises selector resolution + validate-diff + bench in sequence, and ships green when the whole P1 surface is wired correctly.

It mirrors **plan/01-multi-language-three-source.md §4** step-by-step — the "demo script" the plan promised would be runnable end-to-end as the wave gate.

## Scenario

1. `polyglot-repo-go-ts-py` helper — Go + TS + Python tree, each with `CheckoutValidator`.
2. `graph-harness init` + install demo overlay.
3. Three steps, in order:
   - `selectors test CheckoutValidator --json` — asserts `CheckoutValidator` + `matches` (the same `.gh` resolves across all three languages).
   - `echo "" | validate-diff --json` — empty-diff sanity check, asserts `0 findings`.
   - `bench --scenario 1 --regime mature` — asserts `detection_axis` (the bench actually runs across the three fixtures and the result lands).

## What's covered vs deferred

- **Covered:** the integration path from selector resolution through validate-diff through bench in one run.
- **Soft assertions only:** the spec asserts shape + presence, not specific scores or finding kinds (those live in the per-capability specs). The capstone is a "doesn't fall over" gate, not a finely-tuned correctness test.
- **What plan §4 promises and isn't asserted here:** "rename in each → re-anchor via fingerprint fallback; LSP / SCIP conflict surfaces in Conflicts panel." Those are exercised respectively by `validate-diff/reanchor-on-rename.yaml` and `code/three-source-disagreement-emits-symbol-disambiguation.yaml`. Repeating them in the capstone would just create duplicate maintenance.

## Why the empty-diff sanity check

Empty input is a class of regression a green CI miss easily — pipelines often crash on empty input or emit a phantom finding. Asserting `0 findings` on empty input is a 5-line guard that catches that whole family.

## Reference

- `plan/01-multi-language-three-source.md` §4 — demo script this spec implements
- `plan/01-multi-language-three-source.md` §3 — gate criteria (smoke test, demo)
- `plan/phase-1-gaps.md` §1 P1.K — smoke + docs status
- `tests/smoke/p1/demo.sh` — manual / CI-fallback wrapper
- Per-capability specs: `code/three-source-{agreement,disagreement}*.yaml`, `validate-diff/reanchor-on-rename.yaml`, `bench/scenario-1-three-languages.yaml`
