# selector-language-id-anchor

> A selector with `anchor language_id "<lang>"` filters resolution to entities tagged with that language. Same `qualified_name` → different outcomes depending on the language filter.

**Wave:** selectors
**Tags:** selectors, dsl, language-id
**Spec:** `selector-language-id-anchor.yaml`

## Why this spec exists

`language_id` is unique among anchors in two ways:

1. It's a **filter**, not a scoring rung. A `language_id` mismatch is binary (skip or keep) — there's no "0.7 confidence Go-ness". The trace renders it as `"filter active"` rather than as a numeric score.
2. It's **always available**, regardless of the source-class — even tree-sitter-only entities carry `language_id`. So this anchor exercises the filter path independent of LSP/SCIP availability, which keeps the test hermetic.

Polyglot workspaces routinely have the same logical name across languages — a `Validator` class in TS *and* Python. Without `language_id` filtering, a selector targeting "the TS validator" relies on lexical accidents (file paths, qualified-name prefixes). With the filter, the user expresses intent directly.

The spec exercises both polarities of the filter (match → bound, mismatch → unresolved) plus the no-filter baseline (resolution proceeds unconstrained, no `language_id` rung in the trace).

## Scenario

1. `polyglot-repo-go-ts-py` helper — Go + TS + Python tree, each with a `CheckoutValidator.validate` under their own qualified-name namespace.
2. `graph-harness init`.
3. **Inline overlay** with three selectors targeting the **same** `qualified_name "validator.CheckoutValidator.validate"`:
   - `ValidatorTS` — `language_id "typescript"`.
   - `ValidatorWantsGoButTSEntity` — `language_id "go"` (a deliberate mismatch — the TS entity has the qualified name; the filter says go, so the result must be unresolved).
   - `ValidatorAny` — no language filter.
4. Three `selectors test --json --explain` invocations:
   - `ValidatorTS` → `outcome: bound`, `language_id: typescript`, trace contains `kind: language_id` + `filter active`.
   - `ValidatorWantsGoButTSEntity` → `outcome: unresolved`, message mentions `language_id filter`.
   - `ValidatorAny` (text trace) → trace must NOT contain the string `language_id` (no rung was needed).

## What's covered vs deferred

- **Covered:** match / mismatch / no-filter — three polarities of the filter.
- **Not asserted:** anchor *ordering* relative to `language_id`. The current implementation evaluates the filter early (before primary anchors) for efficiency; reordering wouldn't change the semantic outcome but would change the trace layout. If a future ordering change is intentional, it'll need a trace-format spec amendment.

## Reference

- SPEC §3.1 — anchor surface
- `plan/phase-1-gaps.md` §1 P1.F — resolver audit
- `internal/code_core/resolver/` — resolver implementation
- Sister: `selectors/multi-anchor-selector-roundtrip.yaml` (DSL surface), `selectors/explain-anchor-ladder.yaml` (full trace contract)
