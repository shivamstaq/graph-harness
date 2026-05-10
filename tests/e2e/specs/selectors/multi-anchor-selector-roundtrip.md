# multi-anchor-selector-roundtrip

> The DSL accepts the **full multi-anchor surface** — `qualified_name`, `function_signature sig(...)`, `body_hash`, `call_neighborhood {…}`, `ast_hash`, `symbol_fingerprint`, `path_glob` (as `fallback`), plus the `thresholds {…}` block — and renders back to canonical `.gh` that re-parses cleanly.

**Wave:** selectors
**Tags:** selectors, dsl, p1t21, p1t22, p1t23, p1t24
**Spec:** `multi-anchor-selector-roundtrip.yaml`

## Why this spec exists

This is the **DSL surface contract** for P1.E (P1.T21–T24). Every anchor kind in SPEC §3.1 must round-trip through the parser and pretty-printer without loss. The risk: a parser-side regression that accepts a new form but emits a slightly-different canonical (e.g. `sig(Money,Card)` vs `sig(Money, Card)`) breaks selector versioning — the same logical selector produces different stored text and so different stable IDs across upgrades.

The spec exercises four representative anchor surfaces, each of which has bitten previous implementations:

1. **`function_signature sig(Args) -> Ret`** — paren/arrow tokenization is non-trivial.
2. **`call_neighborhood { callers: [...] callees: [...] }`** — the only anchor with an inner block syntax.
3. **`thresholds { bound: 0.95 reanchored: 0.75 ambiguous_zone: [0.55, 0.75] }`** — a non-anchor block; tests the thresholds-as-first-class-clause story.
4. **`fallback path_glob "..."`** — the `fallback` marker in front of a path-glob (path globs are degraded selectors-of-last-resort and have to be tagged as such).

## Scenario

1. `go-module-empty` helper — minimal repo (the DSL parser doesn't need source).
2. `graph-harness init`.
3. Four `graph-harness query 'selector S { ... }'` invocations, one per surface form. Each asserts the relevant tokens appear in the rendered output:
   - signature: `function_signature`, `sig(Money, Card)`, `AuthResult`.
   - call_neighborhood: `call_neighborhood`, `callers`, `callees`.
   - thresholds: `thresholds`, `bound: 0.95`, `ambiguous_zone: [0.55, 0.75]`.
   - fallback: `fallback path_glob`.

## What's covered vs deferred

- **Covered:** parse-then-render roundtrip for the high-leverage anchor surfaces.
- **Not asserted here:** byte-identical roundtrip across two passes (parse → render → parse → render). That's the property-test domain — the spec exists in the gotit e2e wave; the property test for full byte-identity lives in `internal/dsl/parser_test.go`.
- **No `cgo`/`tree-sitter` requires:** the parser is pure Go (no language-server / tree-sitter dependency). Helps this spec run on minimal CI.

## Reference

- SPEC §3.1 — selector / anchor surface
- SPEC §11.2 — DSL grammar
- `plan/phase-1-gaps.md` §1 P1.E — DSL parser audit
- `internal/dsl/parser/` — parser implementation
- `internal/dsl/printer/` — canonical pretty-printer
- Sister: `selectors/selector-language-id-anchor.yaml` (a specific anchor's runtime behavior)
