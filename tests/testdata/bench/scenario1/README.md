# Scenario 1 — auth-sensitive edit (polyglot)

The headline P1 bench scenario: a developer touches a flow-scoped
function in three languages without acknowledging the flow. The validator
must detect the touch and emit a `flow_unreviewed` finding for each
language variant.

## Variants

- `go/`  — pure Go module with `CheckoutValidator.Validate` and a
  callsite invoking it. Mirrors the P0 fixture.
- `ts/`  — Node + Express server with a `CheckoutValidator.validate`
  method that gates a checkout route.
- `py/`  — FastAPI service with a `CheckoutValidator.validate` method
  that gates a checkout endpoint via dependency-injection auth.

Each variant ships:

- The repo state at the snapshot kernel-seq the bench runner replays.
- The flow-scoped function's path so `code.core` ingestion finds it.
- The `target.diff` file: an unreviewed edit to the flow-scoped
  function. Loading it through `change.process` should produce the
  expected `flow_unreviewed` finding.

## Oracle ground truth

Each variant's `oracle.json` declares the expected findings the bench
runner scores against. Format mirrors `internal/bench.ExpectedFinding`.

## Cross-language scoring

The P1 bench gate (plan §3 criterion 10) requires ≥ 0.8 oracle pass
rate per language. With three variants × one expected finding each =
three matches; `detection_axis.score == 1.0` when all are caught.
