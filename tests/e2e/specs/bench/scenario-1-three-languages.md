# scenario-1-three-languages

> Phase-1 bench scenario 1 (auth-sensitive edit) scored across **all three** language variants in one invocation. Asserts the cross-language aggregate fold and the per-language detection-axis scores hit the gate-bar.

**Wave:** bench
**Tags:** bench, polyglot, p1t41
**Spec:** `scenario-1-three-languages.yaml`

## Why this spec exists

This is **gate criterion 10** (plan §3) — "oracle pass rates ≥ 0.8 per language". The bench harness is what graduates the wave: if `bench --scenario 1 --regime mature --language all` doesn't return a clean aggregate score of 1.0 across Go / TypeScript / Python, P1 isn't shippable.

The fixture under `tests/testdata/bench/scenario1/<lang>/` is the closest thing the project has to **production data** — three real source trees (Go: 20 LOC, TS: 34 LOC, Py: 43 LOC), each carrying a `target.diff` that introduces an auth-sensitive edit and an `oracle.json` listing the expected findings. The bench loads each variant, runs change.process against `target.diff`, and scores the output against the oracle.

The spec exercises both the aggregate (`--language all`) and single-language (`--language go`) modes because the JSON shape differs between them — `all` carries `per_language.<lang>` + `aggregate`; single-language emits the bare per-scenario shape (back-compatible with pre-P1 bench output).

## Scenario discovery

The bench command discovers `tests/testdata/bench/scenario1/` via a fallback chain (described in the YAML's inline comment):

1. `--scenario-root <path>` — explicit override.
2. `cwd` walk-up.
3. Project-root walk-up from cwd.
4. `GRAPH_HARNESS_E2E_PROJECT_ROOT` env var (set by the gotit runner).

Path 4 is what makes this spec work from a tmpdir — the gotit runner stages the workspace under a temporary directory; the bench would otherwise have nowhere to look. The `go-module-empty` helper supplies a stand-in workspace because gotit needs *some* repo for its setup invariants, even though the bench reads the actual source from `$GRAPH_HARNESS_E2E_PROJECT_ROOT/tests/testdata/`.

## Scenario

1. `go-module-empty` helper — stand-in workspace.
2. `graph-harness bench --scenario 1 --regime mature --language all` — asserts:
   - `per_language` envelope with `go`, `typescript`, `python` keys.
   - `aggregate` block with `detection_axis.score: 1` (1.0 = every variant detected its expected `flow_unreviewed:CheckoutValidation` finding).
   - `flow_unreviewed:CheckoutValidation` finding string appears (once per language).
3. `graph-harness bench --scenario 1 --regime mature --language go` — asserts the bare per-scenario shape (no `per_language`/`aggregate` envelope) but with `detection_axis` and the same finding string.

## What's covered vs deferred

- **Covered:** the aggregate fold, per-language scoring, single-language back-compatibility.
- **Watchpoint:** Plan §3 gate 10 status is "partial — bench fixture exists but oracle assertions are minimal" (plan/phase-1-gaps.md §10). The minimal-oracle concern is that the fixtures could pass the gate without exercising the multi-source path deeply. Improving oracle rigor is tracked but not blocking ship.
- **Untracked bench boilerplate** (plan/phase-1-gaps.md §1 Barrier 6) was committed in the P1.J closure and the §17 closure ledger marks T59 / §4.4 as ✅. CI sees the `.toml` files now.
- **Deferred (P6):** cold-start ≤90s on a 50K-symbol repo (gate 7). This bench is a correctness gate, not a perf gate; perf instrumentation is explicitly P6.

## Reference

- `plan/phase-1-gaps.md` §1 P1.J — bench audit
- `plan/phase-1-gaps.md` §10 gate 10 — gate criterion this spec asserts
- `plan/01-multi-language-three-source.md` §3 — bench gate definition
- `cmd/bench-rebake/` — body_hash regeneration tool
- `tests/testdata/bench/scenario1/{go,ts,py}/` — fixtures
- Sister capstone: `demo/tracer-multi-lang-demo.yaml`
