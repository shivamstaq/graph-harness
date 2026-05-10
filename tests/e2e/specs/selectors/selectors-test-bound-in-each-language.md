# selectors-test-bound-in-each-language

> A single selector with multi-anchor declarations resolves independently in Go, TS, and Python repos when those toolchains are present.

**Wave:** selectors
**Tags:** selectors, dsl, multi-anchor, p1t26
**Spec:** `selectors-test-bound-in-each-language.yaml`

## Why this spec exists

This spec is the **multi-language smoke** for selector resolution (P1.T26): one `.gh` overlay, three language variants, every language must resolve. It complements `selector-language-id-anchor.yaml` (which exercises the *filter* polarity); here the same selector hits all three without a language filter.

It's a deliberately minimal assertion — just `outcome: bound` indirectly via `contains "CheckoutValidator"`. Heavier per-language assertions live in `extract/lsp/lsp-extracts-{go,ts,python}-functions.yaml`. This one's job is "the same `.gh` works everywhere", which is the cross-language ergonomic contract for selector authoring.

## Scenario

1. `polyglot-repo-go-ts-py` helper — Go + TS + Python tree, each with `CheckoutValidator.validate`.
2. `graph-harness init` + install the demo `.gh` overlay (one selector, multi-anchor).
3. `selectors test CheckoutValidator --json` — asserts `CheckoutValidator` appears in the result. The spec deliberately doesn't pin a single qualified_name match — different languages produce different qualified-name strings; the demo overlay's anchor ladder accommodates all three.

## What's covered vs deferred

- **Covered:** the lowest-bar multi-language resolution.
- **Not exercised here:** strong probes per language (those are the `lsp-extracts-*` specs). This one is an integration smoke; per-language correctness is verified separately.
- **Gating:** `cgo` + `tree-sitter` + `feature:p1-multi-language`. No LSP/SCIP requires — tree-sitter is sufficient for the bare resolution.

## Reference

- `plan/phase-1-gaps.md` §1 P1.E / P1.F — DSL parser + resolver audit
- `internal/code_core/resolver/` — resolver
- Per-language strong probes:
  - `extract/lsp/lsp-extracts-go-functions.yaml`
  - `extract/lsp/lsp-extracts-ts-functions.yaml`
  - `extract/lsp/lsp-extracts-python-functions.yaml`
