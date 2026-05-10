# doctor-json-canonical

> `graph-harness doctor --json [--verbose]` emits the canonical `DetectionReport` envelope: workspace_root, summary, coverage[], tools[]. The slim default form drops probe_chain + source; `--verbose` carries the full chain.

**Wave:** doctor
**Tags:** doctor, p1l
**Spec:** `doctor-json-canonical.yaml`

## Why this spec exists

JSON is the **machine contract** for detection (plan/phase-1-gaps.md §6 decision Q6: "JSON-first DetectionReport as canonical"). Three downstream consumers depend on it:

1. **CI / agents** invoking `doctor --json` to gate runs.
2. **Daemon** serving the same payload over `health.extractors` JSON-RPC.
3. **MCP** adapter exposing `gh://doctor` to introspection tools (see `internal/mcp/adapter.go`).

If any of those drift in shape — a renamed key, a missing `class`, a flattened `coverage[]` — every consumer silently breaks. The spec pins the envelope keys directly so a tag rename (e.g. PascalCase regression on JSON marshalling) trips the test immediately.

The slim vs. verbose split exists because `probe_chain[]` is verbose for terminal humans but essential for debugging in CI. Slim is the default; `--verbose` is the opt-in that carries `probe_chain` + `source` per tool.

## Scenario

1. `polyglot-with-detect-stubs` helper stages Go + TS + Python with stub binaries.
2. `graph-harness init` scaffolds the workspace.
3. `graph-harness doctor --json --verbose` — assert the **full envelope**: `workspace_root`, `summary`, `coverage`, `tools`, all three `language` ids, all three tool `class` values (`lsp` / `scip` / `parser`), both `status` values (`available` for stubs, `embedded` for tree-sitter), `probe_chain`, `source`. The `project_local` source value confirms project-aware probe chains fire.
4. `graph-harness doctor --json` (slim) — same envelope keys, but `probe_chain` must NOT appear (negative assert).

## What's covered vs deferred

- **Covered:** envelope keys, slim/verbose distinction, every primary status enum value.
- **Deferred:** strict JSON-Schema validation (P2). Today the spec uses substring assertions; a `--schema` flag with full structural validation lands when DetectionReport stabilizes.
- **Deferred:** `ExtractorAvailable` state-transition events (now-installed) — explicitly out of scope per plan/phase-1-gaps.md §9.

## Reference

- `plan/phase-1-gaps.md` §8.1 — `DetectionReport` / `ToolReport` shape
- SPEC §6.18 — tooling posture
- `internal/source_live/detect/detect.go` — types backing the JSON
- `internal/cli/doctor.go` — renderer
- Sister: `doctor-text-output-shape.yaml` (human shape)
