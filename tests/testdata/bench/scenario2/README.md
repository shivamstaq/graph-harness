# Scenario 2 — polyglot Kafka event-payload propagation

The Pass-2 bench scenario: a Go producer publishes the `order.created` Kafka
event; three downstream entities reference the same topic — a TypeScript
consumer in `apps/web/`, a Python audit consumer in `pipelines/audit/`, and
a Jest contract test in `tests/contract/`. Mutating the Go `OrderCreated`
payload (add field, rename field, remove field) must produce a
`missing_dependent_update` finding listing all three dependents.

## Fixture layout

- `services/order/`   — Go publisher (segmentio/kafka-go style). Owns the
  `OrderCreated` struct definition the diffs mutate.
- `apps/web/`         — TS consumer using `kafkajs.subscribe("order.created")`.
- `pipelines/audit/`  — Python consumer using `confluent-kafka.subscribe`.
- `tests/contract/`   — Jest spec asserting the `order.created` payload shape.

## Workspace + overlay

- `.graph-harness/graph-harness.toml` — minimal workspace skeleton.
- `.graph-harness/overlay/order_event.gh` — declares the
  `OrderCreatedEvent` selector + `OrderCreatedPropagation` flow.

## Seed + oracle

- `seed.json` — pre-seeds the framework producer + dependent entities + the
  reverse-index selector binding the runner needs. The bench harness applies
  this AFTER tree-sitter ingestion so the `kafka:order.created` linking key
  is reachable by `change.process` Stage 5 BFS.

- `oracle.json` — per-diff expected `missing_dependent_update` findings.
  Dependents are tagged by language so the bench runner can score the
  detection axis per language (`go`, `typescript`, `python`).

## Target diffs

- `diffs/add_field.diff`    — adds `transaction_id string` to `OrderCreated`.
- `diffs/rename_field.diff` — renames `OrderID` to `OrderUUID`.
- `diffs/remove_field.diff` — removes `CustomerID`.

Each must produce the same finding shape: one `missing_dependent_update`
with the three dependents in evidence.
