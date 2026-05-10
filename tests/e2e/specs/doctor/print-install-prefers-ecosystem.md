# print-install-prefers-ecosystem

> `graph-harness doctor --print-install` emits a **project-aware** install command per missing tool: `pnpm-lock.yaml` → `pnpm add -g`; `.venv` → `pipx install pyright`; Go → `go install <module>@latest`.

**Wave:** doctor
**Tags:** doctor, install-hints, p1l
**Spec:** `print-install-prefers-ecosystem.yaml`

## Why this spec exists

The harness is a **detector + orchestrator, never a supplier** (plan/phase-1-gaps.md §7 Q2). It will not run package managers as subprocesses. But it *will* tell the user the right one-liner — and "right" depends on the project's existing toolchain. Telling a `pnpm` workspace user to run `npm i -g` is wrong even if `npm` works: it splits the install across two managers and makes upgrades fragile. Telling someone with a `.venv/` to `pip install pyright` system-wide is worse — it pollutes the system environment instead of using the venv they already configured.

The probe order for the *install hint* mirrors the probe order for *detection* (plan/phase-1-gaps.md §8.2): the install command targets the same manager the detector would have found a binary under, had one been present. This keeps detection and install instructions symmetric: the binary that would *be* found is the binary that *will* be installed by the recipe.

## Scenario

1. `polyglot-missing-extractors` helper stages a polyglot tree but **without** stubs — primary extractors (gopls, tsserver, pyright, scip-*) are all missing.
2. `graph-harness init`.
3. Each step runs with `PATH=/usr/bin:/bin` (sanitized) so the host's real installs don't accidentally satisfy the probe.
4. `graph-harness doctor --print-install` — three independent probes:
   - TS missing → `pnpm add -g typescript-language-server` and `pnpm add -g @sourcegraph/scip-typescript` (the helper plants `pnpm-lock.yaml`).
   - Python missing → `pipx install pyright` (the helper plants `.venv/`).
   - Go missing → `go install github.com/scip-code/scip-go/cmd/scip-go@latest` (always — Go has one canonical install path; scip-go was renamed from `sourcegraph/` to `scip-code/` mid-2025 per SPEC §6.17).

## What's covered vs deferred

- **Covered:** pnpm + pipx + go-install ecosystems.
- **Not exercised here:** `bun.lockb` → `bun add`, `yarn.lock` → `yarn global add`, `uv tool install`. The detector code supports these (`internal/source_live/detect/detect_typescript.go` + `detect_python.go`); add fixtures + asserts when they become load-bearing.
- **Deferred (P2):** `--print-install --shell=fish|zsh` (today emits bash-shaped one-liners).

## Reference

- `plan/phase-1-gaps.md` §8.1 — `InstallHint` shape (`Manager`, `Command`, `Preferred`, `Reason`)
- `plan/phase-1-gaps.md` §7 Q2 — "detector and orchestrator, never supplier" rationale
- `internal/source_live/detect/detect_typescript.go` — pnpm/bun/yarn install-hint preferences
- `internal/source_live/detect/detect_python.go` — pipx/uv/pip preferences
- `internal/cli/doctor.go` — `--print-install` renderer
