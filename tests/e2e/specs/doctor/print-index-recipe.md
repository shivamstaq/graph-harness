# print-index-recipe

> `graph-harness doctor --print-index-recipe` emits SCIP-generation commands for every detected language. P1 substitute for the deferred `graph-harness index` Go subcommand.

**Wave:** doctor
**Tags:** doctor, scip-recipe, p1l
**Spec:** `print-index-recipe.yaml`

## Why this spec exists

`graph-harness index` (a Go subcommand that would orchestrate `scip-go` / `scip-typescript` / `scip-python` as subprocesses) was **deferred to P2** (plan/phase-1-gaps.md §7 decision Q7). Subprocess orchestration with cancellation, staleness reporting, and per-language argument quirks deserves its own design pass.

The compromise: `--print-index-recipe` emits *the exact shell command* the user can copy-paste, with the project-local binary path baked in. So when the detector finds `vendor/bin/scip-go`, the recipe uses *that* path — not whatever `scip-go` happens to land on `$PATH`. This sidesteps "I have three scip-go installs and the recipe used the wrong one" before it can become a bug report.

If this output drifts (wrong flag, wrong binary path, wrong output filename, missing `mkdir`), every user following the README quickstart gets a broken `.scip-index/`.

## Scenario

1. `polyglot-with-detect-stubs` helper stages all three languages with project-local stubs (notably `vendor/bin/scip-go`, `node_modules/.bin/scip-typescript`, `.venv/bin/scip-python`).
2. `graph-harness init`.
3. `graph-harness doctor --print-index-recipe` — asserts the command stream contains:
   - `mkdir -p .scip-index` setup line.
   - Go: `vendor/bin/scip-go --module-root=. --output=.scip-index/index.go.scip` (project-local path; flags match upstream scip-go).
   - TS: `node_modules/.bin/scip-typescript index --infer-tsconfig --output=.scip-index/index.ts.scip` (project-local path; `--infer-tsconfig` is the standard non-monorepo invocation).
   - Python: `.venv/bin/scip-python index --output=.scip-index/index.py.scip .` (positional `.` for the source root).

## What's covered vs deferred

- **Covered:** the three canonical languages with project-local SCIP indexers.
- **Not exercised:** missing-tool output. When an indexer is missing the recipe falls back to the bare binary name and the install hint ships in `--print-install` (see sister spec).
- **Deferred (P2):** `graph-harness index` as a Go subcommand. When it lands the recipe stays as a transparency surface ("show me what you'd run") even if the harness orchestrates it directly.

## Reference

- `plan/phase-1-gaps.md` §7 Q7 — deferral decision and recipe rationale
- `plan/phase-1-gaps.md` §3 — original install recipe these commands derive from
- `internal/cli/doctor.go` — recipe renderer
- Sister: `print-install-prefers-ecosystem.yaml` (install hints for missing tools)
