# config-override-resolves-basedpyright

> `.graph-harness/config.toml` `[lsp.python] server = "basedpyright"` is parsed and surfaced via the workspace config loader. The override flips the canonical Python LSP id without code changes (SPEC §6.18).

**Wave:** doctor
**Tags:** doctor, config-override, p1l
**Spec:** `config-override-resolves-basedpyright.yaml`

## Why this spec exists

A canonical default per language is essential — the harness has to pick *one* LSP per language so error reporting, install hints, and provenance attribution stay coherent (plan/phase-1-gaps.md §7 Q9). The defaults are `gopls` / `typescript-language-server` / `pyright` / `rust-analyzer`. But these aren't politics-free: `basedpyright` is a community fork some teams prefer; `vtsls` ships VS Code-style diagnostics; `pylsp` exists for plugin-driven workflows.

The escape hatch is a **per-workspace TOML override** — `[lsp.<lang>] server = "..."` resolves against a known-server registry. This spec asserts the **plumbing** of the override: the file is parseable on disk and carries the expected key/value. It does **not** yet assert that `doctor` swaps the probe chain dynamically — that's an explicit P2 graduation (see comment in the YAML's `setup:` block).

## Scenario

1. `python-with-basedpyright-override` helper plants `.graph-harness/config.toml` with `[lsp.python] server = "basedpyright"` already populated.
2. **No init step** — the helper IS the setup; the spec verifies the file content survives helper staging.
3. `cat .graph-harness/config.toml` — asserts `[lsp.python]` table heading and `server = "basedpyright"` value.

## What's covered vs deferred

- **Covered:** override file is staged + persisted; tag/value parse cleanly.
- **Deferred (P2):** dynamic probe-chain swap. Today the registry has `KnownLSPServers` entries for basedpyright/pyright/pylsp/jedi-language-server/vtsls (`internal/source_live/detect/config.go`), but `doctor` still probes the default tool name. When that wiring lands, this spec extends to assert `doctor --json --language=python` reports `"name": "basedpyright"` after the override.

## Reference

- `plan/phase-1-gaps.md` §8.5 — workspace config schema
- `plan/phase-1-gaps.md` §7 Q9 — single-canonical-LSP-with-override decision
- SPEC §6.18 — tooling posture
- `internal/source_live/detect/config.go` — `WorkspaceConfig` + `KnownLSPServers`
- `tests/e2e/helpers/detect_stubs.go` — `PythonWithBasedpyrightOverride` helper
