# lsp-extracts-python-functions

> When `pyright` is on PATH, source.live's LSP host extracts Python symbols and code.core stores entities with `extractor:lsp:pyright` provenance.

**Wave:** extract
**Tags:** extract, lsp, p1t04
**Spec:** `lsp-extracts-python-functions.yaml`

## Why this spec exists

Per-language LSP smoke for Python (P1.T04), structurally identical to `lsp-extracts-go-functions.yaml` and `lsp-extracts-ts-functions.yaml`. Python has the **most demanding** of the three LSP integrations because:

1. **`pyright` startup is heaviest** of the canonical three — without venv detection it type-checks against the global interpreter and emits a flood of false `SymbolDisambiguation` events (plan/phase-1-gaps.md §1, Barrier 5).
2. **The qualified_name rule is dotted-path** — `checkout/validator.py` → `checkout.validator.<class>.<method>`. Different from TS's basename rule and from Go's package rule. The spec hard-codes `checkout.validator.CheckoutValidator.validate` to catch any regression in the Python parser's module-path emission.
3. **Venv resolution** is the load-bearing piece — `internal/source_live/lsp/pyright.go` wraps the generic driver in a `pyrightDriver` that calls `detect.ResolveVenv` and merges `python.pythonPath` into `initializationOptions` at spawn (plan/phase-1-gaps.md §17 T51, closes §4.3).

## Scenario

1. `python-module-with-checkout-validator` helper — Python package with `checkout/validator.py` defining `class CheckoutValidator: def validate(self, cart): ...`.
2. `graph-harness init` + install `demo.checkout.gh` overlay.
3. `graph-harness selectors test CheckoutValidator --json` with LSP enabled — asserts:
   - `outcome: bound`.
   - `qualified_name: checkout.validator.CheckoutValidator.validate` (dotted-path module rule).
   - `via_anchor: qualified_name`.
4. `graph-harness code provenance "CheckoutValidator" --language python --json` — provenance on the class type (LSP-only). Asserts `source_class: live_lsp` + `produced_by: extractor:lsp:pyright`.

## What's covered vs deferred

- **Covered:** pyright lifecycle, dotted-path qualified_name rule.
- **Covered indirectly:** venv resolution — when `pyright` is invoked from a workspace with `.venv/`, the `pyrightDriver` wrapper passes `python.pythonPath: <venv>/bin/python` automatically. The hermetic helper used here doesn't have a venv, so the wrapper falls through cleanly. The dedicated venv-resolution spec (plan/phase-1-gaps.md §13 `pyright-uses-detected-venv.yaml` Tier-3) is gated on a real pyright install.
- **Not exercised:** `[lsp.python] server = "basedpyright"` override. The TOML loader is verified by `doctor/config-override-resolves-basedpyright.yaml`; dynamic probe-chain swap is P2.

## Reference

- `plan/phase-1-gaps.md` §1 Barrier 5 — venv detection rationale
- `plan/phase-1-gaps.md` §17 T51 — venv-resolution implementation closing §4.3
- `internal/source_live/lsp/pyright.go` — `pyrightDriver` wrapper + `ResolveVenv` merge
- `internal/source_live/lsp/pyright_test.go` — unit tests for the merge helper
- Sister specs: `lsp-extracts-go-functions.yaml`, `lsp-extracts-ts-functions.yaml`
