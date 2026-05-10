# The layered graph

Graph Harness models a repository as a layered, replayable graph substrate. Each layer owns a distinct class of facts, with its own lifecycle, freshness model, storage strategy, and trust policy. The kernel coordinates those layers through a common event log, query router, selector resolver, snapshot system, and provenance model.

This design keeps the code graph, authored semantics, validation artifacts, review workflow, and historical evolution connected without forcing them into one schema or one storage model.

## Why layers

A repository contains facts that change at different rates and carry different levels of authority. Live editor state, parsed source facts, framework routes, human-authored invariants, validation findings, and review decisions should not share the same mutability or trust assumptions.

Graph Harness uses layers because these concerns diverge along several axes:

- **Mutability**: LSP facts are ephemeral; code facts are derived; overlay content is authored and versioned; process artifacts are append-only.
- **Ownership**: extractors own code facts; teams own flows and invariants; review policy owns promotion decisions.
- **Storage**: source snapshots, traversal indexes, `.gh` overlay files, event logs, and history projections have different access patterns.
- **Freshness**: an editor buffer can be live while an index is still rebuilding; a selector can be bound, reanchored, stale, or unresolved.
- **Trust**: extractor output, human-authored rules, imported content, and agent proposals require different write policies.

The kernel remains small and domain-free. It provides registry, dependency ordering, query routing, selector resolution, event sequencing, provenance folding, snapshot and replay coordination, trust enforcement, and schema migration. Domain semantics live in layers.

## Starter layers

The initial architecture defines seven coarse layers. They are intentionally broad enough for v1 operations, while still allowing future splits without kernel changes.

| # | Layer | Holds | Source | Mutability |
|---|---|---|---|---|
| 1 | `source.live` | Files, modules, symbols, imports, parser snapshots, diagnostics | LSP, tree-sitter, filesystem watchers | ephemeral / cached |
| 2 | `code.core` | Language-level entities and relations: calls, references, definitions, functions, classes, methods, interfaces | Unified facts from LSP, SCIP, and tree-sitter | derived |
| 3 | `code.framework` | Routes, handlers, queues, payloads, jobs, schemas, GraphQL resolvers, contract tests | Framework-specific extractors | derived |
| 4 | `semantic.overlay` | Harnesses, flows, invariants, controls, skills, selectors, runbooks, boundaries, ownership hints, risk labels | Agent, human, importer, and drift-detector authoring routed through review policy | authored / proposed / versioned |
| 5 | `change.process` | Tasks, plans, diffs, touched regions, validation findings, repair instructions, execution traces | Validation pipeline and consumer operations | append-only / session-scoped |
| 6 | `review.queue` | Proposals, state transitions, conflicts, approvals, rejections, evidence | Trust pipeline | append-only with state |
| 7 | `history.evolution` | Commits, entity revisions, moves, renames, splits, merges, co-changes, historical flow changes | Replay over repository history | derived / replayable |

Each layer is installed as a YAML manifest plus a Go module. The manifest declares schema, capabilities, dependencies, storage, snapshot support, emitted events, and trust policy. The kernel validates the manifest and calls the layer through a closed hook set; it does not inspect layer internals.

Future layers such as runtime traces, ownership data, security analysis, build graph, CI results, or ADRs can join the same model by declaring the same contract.

## Cross-layer references

Layer-local IDs are allowed inside a layer. Cross-layer canonical raw IDs are not.

When one layer needs to refer to facts owned by another layer, it uses a selector. A selector is a structured, drift-aware reference that resolves against the graph and returns matches with confidence, provenance, and a resolution outcome.

This rule exists because raw IDs are physical pointers, not durable intent. Symbols move, functions are renamed, files split, indexers change their internal identity model, and team or replay environments may reconstruct different local IDs. If upper layers store lower-layer raw IDs directly, authored overlay content becomes brittle and expensive to migrate.

Selectors preserve intent through multiple anchors. A selector may begin with a qualified name, then fall back to signature, body hash, call neighborhood, path glob, or a graph query. Resolution produces one of five outcomes:

- `bound`: the primary anchor matched with sufficient confidence.
- `reanchored`: the primary anchor missed, but a fallback anchor matched above threshold.
- `ambiguous`: multiple candidates matched; review is required.
- `unresolved`: no candidate matched; review is required.
- `superseded`: a newer entity was identified through rename, split, merge, or other evidence.

The schema rule is enforced at install time. A layer manifest that declares `entity_ref(<other_layer>)` as canonical schema is rejected. Resolved `EntityRef` values may be cached for performance, but selectors remain the authoritative cross-layer reference.

## Global sequence

Every kernel event receives one strictly increasing `seq`. This gives all layers a shared consistency coordinate.

With a single sequence, `consistent at seq N` has one meaning across the entire substrate. Replay is one ordered log, cross-layer invalidation has deterministic ordering, and validation can pin a `validation_seq` that every later stage reads against.

Per-layer sequences would require reconciliation every time a query crossed layer boundaries. Graph Harness avoids that ambiguity by serializing event assignment through the kernel sequencer. For the local-first development workload, the operational cost is acceptable and the consistency model is substantially simpler.

## Provenance

Every cross-layer response carries provenance. The kernel folds constituent provenance into a summary and preserves the underlying details for inspection.

The fold is mechanical:

```text
confidence    = min(confidence_i)
freshness     = worst(freshness_i)
source_class  = union(source_class_i)
inputs        = deduped union(inputs_i)
seq           = max(seq_i)
```

The summary prevents consumers from overstating confidence. The constituent details make the answer explainable during review and debugging. If a validation result combines current `code.core` facts with a possibly stale overlay selector, the response must make that visible.

Hard enforcement uses a risk-aware freshness order. Unknown freshness is not treated as safe; it is high risk for blocking decisions. A result may still be useful for exploration while being unsuitable for merge enforcement.

## Read consistency

Graph Harness supports two read modes.

| Mode | Purpose | Consistency model |
|---|---|---|
| Current | Interactive exploration, IDE views, ad hoc queries | Latest known state per layer; partial cross-layer freshness is allowed and reported |
| Snapshot at `seq N` | CI, validation, plan-vs-diff checks, replay, benchmarks, hard controls | All sub-queries read layer state as of the pinned sequence |

Layers declare snapshot support in their manifest. A layer that cannot serve `as_of_seq_read` can contribute to current exploration, but it cannot be used as the basis for hard enforcement. The validation pipeline pins `validation_seq` after a bounded refresh; stages after that pin read from the same sequence to keep findings reproducible.

## `code.core` unification

The `code.core` layer is derived from three fact sources:

- **LSP**: live editor-state facts, high confidence for open files and active development.
- **SCIP**: committed code-index facts, high confidence when the workspace is clean.
- **tree-sitter**: fast structural parsing, error-tolerant fallback when richer sources are unavailable.

These sources have different strengths and freshness profiles. Graph Harness merges agreement and surfaces disagreement. When sources disagree about what should be the same entity, the conflict is emitted as a `SymbolDisambiguation` event rather than being silently overwritten by source priority.

This prevents interactive tooling, CI, and replay from operating against different hidden versions of the code graph.

## Replay

Any past sequence can be queried if the required layers retain or can reconstruct the relevant state. This makes validation runs reproducible, historical briefs inspectable, and drift decisions auditable.

Layers may compact state past snapshot boundaries according to their manifest. Compacted regions must remain rebuildable from declared replay inputs such as the kernel event log, git history, source artifacts, or external indexes.

## Relationship to validation

The layered graph is the substrate used by `validate-diff`. A validation run maps a diff to touched entities, expands to impacted entities through graph relations, attaches matching overlay content, runs controls and invariants, emits structured findings, and records repair instructions. Because those stages read from a pinned sequence and carry provenance, the same diff against the same sequence produces the same result.

The effect is a repository-level semantic layer that is live enough for development, strict enough for CI, and replayable enough for audit.
