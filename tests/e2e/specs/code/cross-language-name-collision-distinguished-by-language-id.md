# cross-language-name-collision-distinguished-by-language-id

> The same qualified name (`User.save`) appearing in Go and Python materializes as **two distinct entities** because SPEC §6.12's `FunctionID` formula mixes `language_id` into the canonical hash.

**Wave:** code
**Tags:** code, identity, polyglot
**Spec:** `cross-language-name-collision-distinguished-by-language-id.yaml`

## Why this spec exists

This is the **identity correctness** test for code.core under polyglot inputs. SPEC §6.12 defines:

```
FunctionID = sha256(language_id || qualified_name || canonical_signature)
```

If `language_id` were dropped from that hash (a plausible regression in a refactor), then `User.save` in Go and `User.save` in Python would collapse into a single entity with a confused, half-merged provenance. Selectors filtering by language would silently misroute. Cross-language name collisions are common — `User`, `validate`, `save`, `Item`, `Order` all show up across multiple languages in any real polyglot service — so the wrong identity formula would surface as bug reports within days of a multi-language workspace.

The spec asserts the **intended** behavior: the two `User.save` entities persist side-by-side, surface side-by-side under `code list`, and carry distinct canonical IDs.

## Scenario

1. `polyglot-colliding-user-save` helper — Go module with `type User struct{}` + `func (u *User) save() error`, plus a Python module with `class User: def save(self): ...`. Both expose the same qualified name `User.save`.
2. `graph-harness init`.
3. Independent provenance probes:
   - `code provenance "User.save" --language go --json` → `qualified_name: User.save`, `language_id: go`.
   - `code provenance "User.save" --language python --json` → same `qualified_name`, `language_id: python`.
4. `code list --qualified-name "User.save"` — asserts both rows appear (`go`, `python`) and the count is `2 entities`.
5. `code list ... --json` — asserts both `language_id` values present in the JSON output, confirming distinct canonical IDs.

## What's covered vs deferred

- **Covered:** language_id mixed into the FunctionID hash; both entities persist; both surface via list.
- **Tree-sitter only:** the assertion is gated on `feature:p1-extractor-wiring` because the CLI path (`code provenance` / `code list`) routes through `extract.Orchestrator`, whose `Close` previously hung on `scip.Refresher.Stop` when no `.scip-index/` existed (30s timeout). The wiring fix flipped that gate on for all of `code/*`.
- **Not exercised:** SCIP-side identity (the spec is tree-sitter-driven). SCIP-imported entities use the same FunctionID formula; verifying that path is the job of the three-source agreement specs.

## Reference

- SPEC §6.12 — canonical key formulas (FunctionID / TypeID / etc.)
- `plan/phase-1-gaps.md` §1 P1.D — code.core unification audit
- `internal/code_core/identity.go` — FunctionID + TypeID canonicalisation
- `internal/code_core/unify.go` — entity merge using identity
- Sister: `code/three-source-agreement-merges-provenance.yaml` (single-entity convergence under same identity)
