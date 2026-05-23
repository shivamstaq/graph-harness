# Cookbook: framework flows

Phase 2 ships `code.framework` extractors that enrich `code.core` with
*framework-shaped* entities — HTTP routes, event publishers and
subscribers, contract tests, database schema fields. Once those entities
exist, you can write flows whose steps cross framework boundaries (and
process boundaries) without giving up the refactor-stable selectors that
make harnesses durable.

This cookbook is a tour of the patterns that show up most often.

## What a flow that spans framework entities looks like

A flow's steps may target different entity kinds. The publisher of an
event lives in one service, the subscriber lives in another, the
contract test lives next to neither. A single flow ties them together:

```gh
// harnesses/order-created.gh
flow OrderCreatedPropagation {
  description "An order.created event must round-trip through every dependent."

  step PublishOrderCreated targets selector {
    anchor entity_kind "EventPublisher"
    anchor topic_name  "order.created"
  }

  step ConsumeOrderCreated targets selector {
    anchor entity_kind "EventSubscriber"
    anchor topic_name  "order.created"
  }

  step ContractCovered targets selector {
    anchor entity_kind "ContractTest"
    anchor topic_name  "order.created"
  }
}
```

The flow is one authored artifact. When the publisher's payload changes
in a way that breaks the implicit contract, every step that resolves
against the same `topic_name` is in the impacted set, and the validation
pipeline can emit a single `missing_dependent_update` finding per
dependent rather than one per file.

## Selectors that target framework entities

The framework extractors register additional anchor kinds on top of the
ones `code.core` ships. The shape stays the same — `anchor <kind>
<value>` — only the kind names are new:

| Anchor          | Targets                              | Example value                           |
|-----------------|--------------------------------------|-----------------------------------------|
| `entity_kind`   | The framework entity class           | `EventPublisher`, `Route`, `Handler`    |
| `route_pattern` | HTTP route templates                 | `/orders/{id}`                          |
| `route_method`  | HTTP method                          | `POST`                                  |
| `event_name`    | Logical event name (asyncapi-shaped) | `OrderCreated`                          |
| `topic_name`    | Broker topic / subject               | `order.created`                         |
| `schema_field`  | Database column                      | `orders.transaction_id`                 |
| `schema_table`  | Database table                       | `orders`                                |

A selector may combine anchors — the second is a fallback when the first
misses. Confidence is the minimum across resolved anchors:

```gh
selector OrderCreatedPayload {
  anchor   entity_kind "EventPayload"
  anchor   topic_name  "order.created"
  fallback path_glob   "services/orders/**/publisher.go"
  thresholds { bound: 0.90; reanchored: 0.70 }
}
```

## The `missing_dependent_update` finding

`missing_dependent_update` fires when a touched framework entity has
declared dependents that the same diff does not also update. The
pipeline knows the dependents because the extractor that produced the
publisher also produced its subscribers and any contract tests that
mention the same topic, and it links them through
`FrameworkContext.Dependents`.

Each finding carries:

- **`subject`** — the touched publisher (or route, or schema field).
- **`evidence[].detail`** — a human-readable line naming the dependent
  file and what it consumes.
- **`framework_context.dependents`** — the full list of dependent
  entities the extractor knows about, with `entity_kind`, `entity_id`,
  `path`, and the relation kind (`consumes_event`, `references_topic`,
  `reads_column`, …).
- **`repair`** — populated once Phase 3 ships `RepairInstruction`
  synthesis; empty in Phase 2.

Treat the finding as a *coordination prompt*, not a blocking failure
unless your harness raises it. The typical reaction is:

1. Read `framework_context.dependents` to see who else needs to change.
2. Add the matching field / handler / assertion in those dependents.
3. Re-run `validate-diff` — the finding clears when every dependent
   that the extractor knows about is touched in the same diff.

## Common patterns

### HTTP route → handler binding

The HTTP extractors register a `Route` entity per `<method, pattern>`
and link it to its handler with the `bound_to_handler` relation. A flow
that gates auth on a route looks like:

```gh
flow OrderCheckoutAuth {
  description "Every POST /checkout request must run through the auth middleware."

  step Route targets selector {
    anchor entity_kind   "Route"
    anchor route_method  "POST"
    anchor route_pattern "/checkout"
  }

  step Handler targets selector {
    anchor entity_kind "Handler"
    anchor qualified_name "checkout.HandleCheckout"
  }
}
```

A diff that changes the route pattern without updating the handler (or
vice versa) lands a `missing_dependent_update` against the side that
moved first.

### Kafka topic → publisher + subscribers + contract test

This is the worked example the smoke spec exercises end-to-end. One
publisher in Go, two subscribers (TS + Py), one Jest contract test —
all four register against `topic_name: order.created`. Adding a field
to the publisher payload emits three findings, one per dependent. See
[`tests/e2e/specs/phase2/smoke/p2-event-payload-cross-lang.yaml`](../../tests/e2e/specs/phase2/smoke/p2-event-payload-cross-lang.yaml).

### DB schema field → readers + writers + tests

```gh
flow OrderTotalReaders {
  description "Code that reads orders.total must tolerate the column dropping to NOT NULL."

  step Column targets selector {
    anchor entity_kind  "SchemaField"
    anchor schema_field "orders.total"
  }

  step Readers targets selector {
    anchor entity_kind  "Function"
    anchor reads_column "orders.total"   // relation-derived selector
  }
}
```

Schema-derived relations come from the SQL / ORM extractors; refer to
the per-extractor README for the exact relation names each one emits.

## Disabling an extractor

Extractor selection is per workspace. Disable any extractor by name:

```sh
graph-harness extractors list                 # show current state
graph-harness extractors disable route.py.django
graph-harness extractors enable  route.py.django
```

Disabled extractors stop running on the next kernel tick. The framework
entities they previously produced are marked `stale` until the
governing extractor re-runs or is re-enabled.

## Limitations

The Phase 2 extractor set is deliberately narrow. The per-extractor
READMEs under [`extractors/`](../../extractors/) carry the exhaustive
list; the recurring ones are:

- **Typed event payloads (CloudEvents, AsyncAPI, Protobuf schemas) are
  post-v1.** Phase 2 uses string topic equality only; field-level
  schema diffs are detected for SQL columns only.
- **Cross-language topic matching uses string equality.** A topic
  renamed from `order.created` to `OrderCreated` is two distinct
  topics until the extractors learn naming conventions per ecosystem.
- **Subject inference for contract tests is medium-confidence.** A
  Jest `describe("order.created", …)` block is treated as a contract
  test for `order.created`; mismatches between the describe string and
  the actual asserted payload are not detected until typed payloads
  ship.
- **The dependents list is *known* dependents only.** An extractor that
  has not run, or a consumer language with no extractor, contributes
  nothing to `FrameworkContext.Dependents`.

See the per-extractor README for the full list of limitations per
ecosystem.
