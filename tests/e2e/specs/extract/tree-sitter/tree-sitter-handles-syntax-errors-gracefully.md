# tree-sitter-handles-syntax-errors-gracefully

> A workspace containing a Go file with a syntax error must not crash extraction. tree-sitter degrades to extracting what it can from the surrounding well-formed code.

**Wave:** extract
**Tags:** extract, tree-sitter, p1t14
**Spec:** `tree-sitter-handles-syntax-errors-gracefully.yaml`

## Why this spec exists

tree-sitter is the **structural source** — the always-on, always-available baseline (cgo-bundled in `go.mod`, no external install). SPEC §6.11's whole build-order tolerance contract relies on tree-sitter producing *something useful* even when LSP is missing and SCIP indexes are stale or absent.

The risk: tree-sitter grammars produce error nodes around malformed regions, and a naive walker will trip on them. Plan §5 calls this out explicitly as a risk: **"tree-sitter grammar quirks must degrade, not abort"**. If the walker panics on an error node, an unrelated syntax error in some peripheral file silently breaks the whole workspace's indexing.

This spec is the regression guard. It plants a deliberately broken Go file alongside the well-formed `validator.go` and asserts:

1. Selectors still resolve against the *valid* file.
2. `graph-harness status` doesn't crash either — the workspace stays "healthy" from a CLI-surface standpoint even though one source file is unparseable.

## Scenario

1. `go-module-with-checkout-validator` helper — clean Go module.
2. `graph-harness init`.
3. Plant `internal/checkout/broken.go` containing `func bad( {\n  this is not valid Go\n}` — clearly malformed.
4. Install the demo overlay.
5. `graph-harness selectors test CheckoutValidator --json` — asserts:
   - `outcome: bound` (resolution still works against the well-formed file).
   - `qualified_name: checkout.CheckoutValidator.Validate`.
6. `graph-harness status` — asserts exit 0 + `Workspace:` header (no crash, not even a degraded mode).

## What's covered vs deferred

- **Covered:** the Go grammar's behavior on a malformed file. Equivalent specs for TS / Python aren't strictly necessary because the walker abstraction (`internal/source_live/treesitter/parser_*.go`) shares the error-node handling — Go is the canary.
- **Deferred:** assertions on **what was extracted** from the broken file (e.g. partial entity shape). Today the walker either skips the broken file entirely or emits whatever the grammar's recovery yields — we don't assert specifics because the grammar's recovery is upstream behavior we don't control. The contract this spec defends is "doesn't crash + neighbors still work".
- **Tree-sitter `.scm` query files** (`extractors/treesitter/{typescript,python}.scm`) are documentation-only — the runtime walks the AST manually for speed and tolerance to malformed trees. Plan/phase-1-gaps.md §1 Barrier 8 describes this; §4.6 leaves it as documentation-deferred to P2.

## Reference

- `plan/phase-1-gaps.md` §1 P1.C — tree-sitter audit
- `plan/phase-1-gaps.md` §1 Barrier 8, §4.6 — `.scm` query intent vs. runtime walker
- Plan §5 risk — tree-sitter grammar quirks must degrade, not abort
- SPEC §6.11 — build-order tolerance
- `internal/source_live/treesitter/parser_*.go` — manual AST walkers
