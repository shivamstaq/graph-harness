# `extractors/events` — Message-bus event extractors

Pass 1 / 1-Events slice (P2.T14, P2.T15, P2.T16) of
`plan/02-framework-extractors.md`.

This family detects publish / subscribe / topic call sites for the
following message-bus transports across Go, TypeScript, and Python:

| Transport     | Go (library)                                    | TypeScript (library)                     | Python (library)                       |
|---------------|--------------------------------------------------|------------------------------------------|----------------------------------------|
| Kafka         | `segmentio/kafka-go`, `confluent-kafka-go`       | `kafkajs`                                | `confluent-kafka-python`, `kafka-python` |
| NATS          | `nats.go`                                        | `nats.js`                                | `nats-py`                              |
| AMQP/RabbitMQ | `rabbitmq/amqp091-go`, `streadway/amqp`          | `amqplib`, `amqp-connection-manager`     | `pika`, `aio-pika`                     |
| Redis pub/sub | `go-redis`, `gomodule/redigo`                    | `ioredis`, `node-redis`                  | `redis-py`                             |
| AWS SNS/SQS   | `aws-sdk-go-v2`, `aws-sdk-go`                    | `@aws-sdk/client-sns`, `@aws-sdk/client-sqs` | `boto3`                            |

Each (transport, language) pair is one Go package whose `init()` calls
`code_framework.Register(...)`. The 15 registered names are:

```
events.kafka.go        events.kafka.ts        events.kafka.py
events.nats.go         events.nats.ts         events.nats.py
events.amqp.go         events.amqp.ts         events.amqp.py
events.redispubsub.go  events.redispubsub.ts  events.redispubsub.py
events.snsqs.go        events.snsqs.ts        events.snsqs.py
```

## What gets emitted

Each detected call site emits up to three kernel events:

- `EventTopicObserved` — the topic-level `code.framework.Event` row,
  unique by `(transport, name)` across the workspace.
- `EventPublisherAdded` — one per publish call site, anchored to the
  enclosing function's `qualified_name`.
- `EventSubscriberAdded` — one per subscribe call site, anchored to
  the enclosing function (or handler) `qualified_name`.

Per the Pass-0.5-A anchor convention
(`internal/semantic_overlay/anchors/event.go`):

- `Event` entities carry `Kind="Event"`, `QualifiedName=topic_name`,
  and `KindTag="topic:<transport>"` where `<transport>` is one of
  `kafka`, `nats`, `amqp`, `redis_pubsub`, `sns`, `sqs`.
- `EventPublisher` / `EventSubscriber` entities carry their respective
  kinds with `QualifiedName=event_name`.

The `code_framework.MakeContentID` hash deduplicates identical
emissions — re-extracting the same file twice produces byte-identical
payloads and the kernel's compare-before-emit (SPEC §6.21) drops them.

## Selector anchor list

For each `EventPublisher` / `EventSubscriber` the extractor emits a
`SelectorRef` with the following anchor list (in selector-priority
order):

1. `qualified_name` → `"pkg.Func"` (Go) / `"module.func"` (TS/Py) /
   `"module.Class.method"` for methods
2. `event_name` → the topic / subject string
3. `path_glob` → the workspace-relative file path (fallback for
   anonymous closure sites)

For the topic-level `Event` row the anchor list is:

1. `event_name` → topic / subject
2. `entity_kind` → `"Event"`
3. `topic_transport` → `kafka` / `nats` / …

Selector resolution in `semantic.overlay` consumes these anchor
lists verbatim per SPEC §2.2 (selectors are the only legal
cross-layer reference; raw `code.core.Entity.ID` is NEVER carried in
extractor emissions).

## Cross-language matching (P2.T16) — v1

Topic strings are compared by **exact string equality** on the
`(transport, name)` tuple. A Go publisher of
`kafka.NewWriter(kafka.WriterConfig{Topic: "order.created"})` and a TS
subscriber of `kafka.consumer().subscribe({topic: "order.created"})`
collapse to the same `Event` entity because both extractors emit
`MakeContentID(Event, eventAnchor("kafka", "order.created"), …)`
which hashes to the same content-id.

### Known limitations (v1)

- **Non-literal topics are dropped at extract time.** Topics computed
  from env vars, constant pools, struct fields, or template-string
  interpolations produce no entity. Users with this pattern must
  declare the binding manually via selector overrides.
- **No typed-event-registry awareness.** CloudEvents and AsyncAPI
  registries are not parsed; future support is tracked as post-v1 (see
  `plan/02-framework-extractors.md` §5 risk row).
- **Pointer / variable resolution is best-effort.** The Go Kafka
  extractor performs a single-file lookup for the
  `confluent-kafka-go: kafka.TopicPartition{Topic: &topic}` pattern
  where `topic` is a string-literal short-var declaration. Cross-file,
  cross-package, or computed bindings are not chased.
- **AMQP routing-key vs queue-name conflation.** AMQP `publish` takes
  an exchange + routing-key; `consume` takes a queue name. v1 treats
  both as topic-equivalent strings — workspaces with non-trivial
  exchange/queue topologies should override matching manually.
- **AWS ARN/Queue-URL normalization.** SNS topic ARNs and SQS queue
  URLs are normalized to their last segment (e.g.
  `arn:aws:sns:us-east-1:000:order-events` → `order-events`,
  `https://sqs.us-east-1.amazonaws.com/000/audit-queue` →
  `audit-queue`) so cross-language matching works without users
  knowing the account ID or region. This is intentional but means
  same-name topics in different AWS accounts would collide; v1
  accepts this since one workspace typically targets one AWS account.

## Per-extractor capabilities

Every extractor declares:

- `Family = "events"`
- `Languages = [<one of "go"|"typescript"|"python">]`
- `Frameworks = [<library names>]`
- `Fallback = "treesitter_only"` — no LSP / SCIP enrichment in v1.
- `BatchHint = "per_event"` — every input file change is processed
  individually; coalescing happens at the watcher layer.

The dispatcher subscribes to `code.core.FileChanged`; each extractor
filters by file extension before parsing.

## Testing

Per-extractor tests live in `<transport>/<lang>/extractor_test.go`
and exercise a single fixture under `testdata/`. The shared helper
package at `common/testutil/` provides:

- `LoadFixture(t, fixturePath, relPath)` → writes the fixture into a
  temp workspace.
- `RunExtractor(t, ext, relPath, lang)` → invokes `OnEvent` with a
  synthetic `code.core.FileChanged` payload.
- `AssertPublishers(t, events, topics...)` / `AssertSubscribers(...)`.
- `AssertTransport(t, events, want)`.
- `AssertDeterministic(...)` — verifies byte-identical re-extraction.

Run all 15 extractors:

```
go test ./extractors/events/...
```

## Files

- `common/` — shared parsers, scanners, emit helpers, topic-key
  contract.
- `common/testutil/` — test helpers used by every extractor.
- `<transport>/<lang>/extractor.go` — Extractor implementation +
  `init()` registration.
- `<transport>/<lang>/testdata/*.txt` — fixture sources (kept under
  `.txt` so `go vet` does not try to compile them).
- `<transport>/<lang>/extractor_test.go` — per-extractor test
  driving one fixture per supported framework variant.
