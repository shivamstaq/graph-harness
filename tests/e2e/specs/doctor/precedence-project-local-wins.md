# precedence-project-local-wins

> Project-local install locations beat ecosystem-user and PATH. Stubs at `node_modules/.bin/` (TS), `.venv/bin/` (Py), `vendor/bin/` (Go) → doctor reports `source: project_local` for each.

**Wave:** doctor
**Tags:** doctor, precedence, p1l
**Spec:** `precedence-project-local-wins.yaml`

## Why this spec exists

The detector's **probe-chain precedence** (plan/phase-1-gaps.md §8.2) is the single most consequential design decision in P1.L: a user with `gopls` installed three different ways (project-local, `~/go/bin`, `$PATH`) deserves to know exactly *which* binary the harness will spawn. The wrong precedence produces a subtle class of bug — the LSP responds correctly, but it's the wrong version, and a `pyright` upgrade in `~/.local/bin` silently overrides the venv-pinned version that matches the project's typeshed.

This is also the only e2e spec that exercises **project-local resolution for all three languages in one fixture** — every other detect spec collapses to a single language or status path. If the precedence matrix in `detect_{go,typescript,python}.go` regresses, this spec catches it.

The corresponding gate is **gate criterion 13** (plan/phase-1-gaps.md §10): "Tier-2 e2e specs assert project-local precedence for at least Go/TS/Py: a stubbed `node_modules/.bin/typescript-language-server` is chosen over a stubbed `~/.local/share/pnpm/typescript-language-server` even with both present."

## Scenario

1. `polyglot-with-detect-stubs` helper plants stub binaries:
   - `vendor/bin/gopls` (Go project-local)
   - `node_modules/.bin/typescript-language-server` (TS project-local)
   - `.venv/bin/pyright-langserver` (Python project-local)
2. `graph-harness init`.
3. `graph-harness doctor --json --verbose --language=<id>` for each language — asserts:
   - tool `name` matches expected (e.g. `pyright-langserver` for python).
   - `status: available`.
   - `source: project_local` (the canonical signal that probe-chain stage 1 won).
   - resolved path contains the project-local prefix (`vendor/bin/`, `node_modules/.bin/`, `.venv/bin/`).

## What's covered vs deferred

- **Covered:** the three primary languages × their three primary project-local locations.
- **Not exercised here:** the `bun pm bin` / `pnpm bin` / `yarn bin` / `pipx list --json` / `uv tool dir --bin` ecosystem layer between project-local and PATH (covered by `print-install-prefers-ecosystem.yaml`).
- **Deferred (P2):** Rust (`rustup which` → `~/.cargo/bin`).

## Reference

- `plan/phase-1-gaps.md` §8.2 — full per-ecosystem probe order
- `plan/phase-1-gaps.md` §10 gate 13 — gate criterion this spec asserts
- `internal/source_live/detect/detect_go.go` — Go probe order
- `internal/source_live/detect/detect_typescript.go` — TS probe order (full bun/pnpm/yarn/npm matrix)
- `internal/source_live/detect/detect_python.go` — Python probe order with `ResolveVenv` exposed for the pyright driver
- `tests/e2e/helpers/detect_stubs.go` — fixture
