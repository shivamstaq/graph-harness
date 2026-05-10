# doctor-text-output-shape

> `graph-harness doctor` (text mode) prints workspace + languages header, a Coverage banner, one card per language, and a Legend / Next-steps footer.

**Wave:** doctor
**Tags:** doctor, p1l
**Spec:** `doctor-text-output-shape.yaml`

## Why this spec exists

Doctor is the user's **first contact** with detection (SPEC §6.18). The text rendering is the human-facing surface — it has to communicate three things at a glance: which languages the workspace contains, which extractors are available for each, and what to do next when something is missing. If the cards drift in shape (e.g. cells lose alignment, or the renderer starts emitting `(embed)` instead of `bundled`), screenshot-based docs and the README quickstart immediately rot.

This spec is the **shape contract** for the rendered output: it asserts the structural elements (header / banner / cards / footer) without pinning exact pixel widths or tool versions. The companion `doctor-json-canonical.yaml` carries the machine-readable contract; together they let the renderer change cosmetically without breaking either consumer.

## Scenario

1. `polyglot-with-detect-stubs` helper stages a Go + TS + Python tree with stub binaries planted at `vendor/bin/`, `node_modules/.bin/`, `.venv/bin/` (see `tests/e2e/helpers/detect_stubs.go`).
2. `graph-harness init` scaffolds `.graph-harness/`.
3. `graph-harness doctor` runs in default text mode.
4. Asserts the output contains, in order: `Workspace:`, `Languages:`, `Coverage`, language ids (`go`, `typescript`, `python`), at least one tree-sitter cell (`tree-sitter-go` + `bundled`), `Legend`, `Next steps`.
5. Negative-asserts on `(embed)` (the renamed legacy label) and on `…` (truncation marker — cards should never hide content).

## What this spec deliberately does *not* check

- Exact column widths. The renderer has a soft column layout that shrinks gracefully on narrow terminals; locking widths would make the spec brittle on every cosmetic tweak.
- Specific tool versions. The stubs respond to `--version` with placeholder strings; the spec asserts shape, not values.
- ANSI escapes / color. The renderer auto-detects TTY; tests run without a TTY so output is plain.

## Reference

- `plan/phase-1-gaps.md` §8.6 — rendered output contract
- SPEC §6.18 — tooling dependency posture (detector + orchestrator, never supplier)
- `internal/cli/doctor.go` — text + JSON renderers
- `tests/e2e/helpers/detect_stubs.go` — fixture
- Sister: `doctor-json-canonical.yaml` (machine-readable shape)
