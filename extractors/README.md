# Framework extractors

Each extractor enriches the `code.framework` layer with framework-shaped
entities (routes, handlers, event publishers, contract tests, schema
fields, …) derived from `code.core` entities and on-disk source. They
ship as Go modules under `extractors/<family>/<language>/<framework>/`
and register via the `code_framework.Extractor` interface — see
[`internal/code_framework/extractor.go`](../internal/code_framework/extractor.go)
for the contract.

## Families

The Phase 2 Pass 1 set covers six families plus a shared
tree-sitter helper. Each family has one or more per-language /
per-framework variants; the family-level README is the canonical entry
point:

| Family        | Covers                                                              | README                                       |
|---------------|---------------------------------------------------------------------|----------------------------------------------|
| `route`       | HTTP routes and their handlers (Go / TS / Python frameworks)        | [`route/README.md`](route/README.md)         |
| `events`      | Pub/sub publishers, subscribers, topics (Kafka, NATS, AMQP, …)      | [`events/README.md`](events/README.md)       |
| `graphql`     | Operations, resolvers, mutations, subscriptions                     | [`graphql/README.md`](graphql/README.md)     |
| `schema`      | DB tables, columns, ORM models, migrations                          | [`schema/README.md`](schema/README.md)       |
| `test`        | Unit tests, fixtures, contract tests                                | [`test/README.md`](test/README.md)           |
| `generated`   | Generated-artifact detection (codegen sentinels + manifest globs)   | [`generated/README.md`](generated/README.md) |
| `treesitter`  | Shared tree-sitter grammars and walkers used by the families above  | —                                            |

Each family-level README enumerates its supported languages, the
entity kinds it emits, the relations it links, and the per-variant
limitations. Subsequent phases extend the set with `jobs`, `rpc`, and
`config` families on the same contract.

## Enable / disable

Extractor selection is per workspace. The CLI is the source of truth:

```sh
graph-harness extractors list
graph-harness extractors enable  events.kafka.go
graph-harness extractors disable route.py.django
```

Disabled extractors stop running on the next kernel tick; the
framework entities they previously produced are marked `stale` until
they re-run or another extractor takes over.

See [`docs/cookbook/framework-flows.md`](../docs/cookbook/framework-flows.md)
for end-to-end usage patterns.

## Adding a new variant

1. Create `extractors/<family>/<language>/<framework>/` and implement
   the `code_framework.Extractor` interface declared in
   [`internal/code_framework/extractor.go`](../internal/code_framework/extractor.go).
2. Emit only the entity kinds the family's manifest already declares —
   adding a new kind requires bumping
   [`manifests/code.framework.yaml`](../manifests/code.framework.yaml)
   and goes through the usual review process.
3. Register the extractor in the family-level `init.go` and list it
   in the family README.
4. Add a fixture under `internal/bench/fixtures/` that exercises the
   variant end-to-end, and reference it from the smoke spec under
   `tests/e2e/specs/phase2/smoke/`.

## Cross-layer references stay selector-only

`code.framework` follows the same SPEC §2.2 invariant every layer
follows: cross-layer references are *selectors*, not raw IDs. A
framework entity that needs to point at a `code.core:Function` stores a
selector (qualified name + signature + fallback path glob), not the
function's layer-local `entity_id`. This is what keeps the framework
layer stable across renames, file moves, and indexer re-runs — and
what lets `validate-diff` reanchor a stale framework entity to its
moved handler instead of failing closed. Resolved `EntityRef` values
may be cached, but the selector is the authoritative reference. The
kernel rejects manifests that declare `entity_ref(code.core)` as
canonical schema.
