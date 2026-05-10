# strict-extractors-exits

> `graph-harness doctor --strict` exits **3** when any primary extractor (LSP or SCIP) is missing for an in-workspace language. Default exits **0** even with missing tools — build-order tolerance per SPEC §6.11.

**Wave:** doctor
**Tags:** doctor, strict-mode, p1l
**Spec:** `strict-extractors-exits.yaml`

## Why this spec exists

The harness has two competing personalities and they need distinct exit codes:

- **Default (exit 0):** "What's available? Tell me." Build-order tolerance — the harness keeps working with whatever sources it finds, emits `code.core.ExtractorUnavailable` events, and lets downstream code reason about reduced confidence.
- **Strict (exit 3):** "Halt the pipeline if anything is missing." CI gating, release validation — anywhere a missing pyright should *fail* a job rather than degrade silently.

Exit code **3** (not 1, not 2) is intentional: 1 is "generic failure", 2 is "validation failure" elsewhere in the CLI. 3 is reserved for "tooling-coverage shortfall" so CI scripts can branch on it specifically (`exit_code == 3 → mail #ops; else → fail loudly`). This is gate criterion **11** in plan/phase-1-gaps.md §10.

`--strict` and `GRAPH_HARNESS_STRICT_EXTRACTORS=1` are equivalent — env var for CI configs, flag for ad-hoc invocations.

## Scenario

1. `polyglot-missing-extractors` helper — polyglot tree, no stubs, primary extractors missing.
2. `graph-harness init`.
3. `PATH=/usr/bin:/bin` sanitized so the host's real `gopls` / `tsserver` / `pyright` cannot accidentally satisfy probes.
4. `graph-harness doctor` (default) → exit 0; output mentions "missing".
5. `graph-harness doctor --strict` → exit **3**; output contains "primary extractor(s) missing".

## What's covered vs deferred

- **Covered:** the exit-code contract under sanitized PATH.
- **Not asserted:** strict mode behavior on the daemon path (`/health/extractors`). The daemon never exits — it surfaces the same data via the event bus (plan/phase-1-gaps.md §8.7).
- **Not asserted:** strict-mode interaction with `selectors test` / `validate-diff`. Those commands set `EnableDetection: false` by default after the §17 hardening pass, so strict mode is effectively a doctor-only flag in P1. P2 work on `code.core.ExtractorUnavailable` will reopen this.

## Reference

- `plan/phase-1-gaps.md` §8.7 — `--strict-extractors` design
- `plan/phase-1-gaps.md` §10 gate 11 — gate criterion this spec asserts
- `plan/phase-1-gaps.md` §17 — hardening pass on detection opt-in
- SPEC §6.11 — build-order tolerance contract
- `internal/cli/doctor.go` — `--strict` flag wiring + exit-code branch
- `internal/code_core/event_extractor_unavailable.go` — the event strict mode escalates
