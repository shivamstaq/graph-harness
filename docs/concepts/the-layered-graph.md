# The layered graph

Graph Harness does not treat a repo as one giant graph. It treats a repo as a stack of smaller graphs that are connected on purpose.

That stack is the layered graph.

Each layer owns one kind of knowledge about the repo. One layer tracks live source files. Another tracks functions and calls. Another tracks framework concepts like routes or queues. Another tracks human-authored rules and flows. Keeping these separate makes the system easier to update, trust, and explain.

## Why not one graph?

A codebase contains facts that behave very differently.

For example:

- Your editor may know about an unsaved file right now.
- A committed index may know about symbols from the last clean build.
- A human-authored rule may say that payment code must always log audit events.
- A validation run may produce temporary findings for one pull request.
- Git history may show that a function was renamed three weeks ago.

Those facts should not all be stored or trusted the same way. Some are live and temporary. Some are derived from code. Some are authored and reviewed. Some are append-only records of what happened.

Layers let Graph Harness model those differences directly.

## The seven starter layers

| Layer | What it holds | Where it comes from |
|---|---|---|
| `source.live` | Files, parser snapshots, diagnostics, and other live source facts | Filesystem watchers, LSP, tree-sitter |
| `code.core` | Language-level code facts: files, symbols, functions, classes, calls, references, definitions | A merge of LSP, SCIP, and tree-sitter facts |
| `code.framework` | App-level facts: routes, handlers, queues, jobs, schemas, GraphQL resolvers, contract tests | Framework-specific extractors |
| `semantic.overlay` | Authored meaning: flows, invariants, controls, skills, selectors, runbooks, ownership hints, risk labels | Humans, agents, importers, and drift detectors through review |
| `change.process` | Per-change work: tasks, plans, diffs, touched regions, validation findings, repair instructions, execution traces | The validation pipeline and consumer operations |
| `review.queue` | Proposed facts, conflicts, approvals, rejections, and supporting evidence | The trust pipeline |
| `history.evolution` | Commits, renames, moves, splits, merges, co-changes, and historical flow changes | Replay over git history |

You can think of each layer as a department with its own job. The kernel is the coordinator. It routes queries, records events, manages snapshots, and enforces trust rules, but it does not need to understand what a route, invariant, or queue consumer means.

## Layers are boundaries

A layer is not just a folder or namespace. It is a boundary for several things:

- **Ownership**: which code is responsible for creating and updating the facts.
- **Lifecycle**: whether facts are temporary, derived, authored, or append-only.
- **Storage**: where facts live and how they are indexed.
- **Freshness**: whether facts are live, current, dirty, stale, or unavailable.
- **Trust**: whether a source may write directly or must go through review.

This matters because a live editor fact and a reviewed human rule should not have the same authority. A coding agent can propose a new invariant, but that proposal should not silently become trusted project policy. It should enter the review queue first.

## How layers refer to each other

Layers do not point at each other using raw internal IDs.

Instead, cross-layer references use selectors.

A selector is a durable description of what you mean. For example, instead of saying "the function with internal ID 1234," a selector might say:

```text
the function named payments.PaymentService.authorize,
or the function with this signature,
or the code near this stable body fingerprint
```

That is important because code moves. Functions get renamed. Files split. IDs can change. A selector can be resolved again later and can report whether it is still bound, reanchored, ambiguous, unresolved, or superseded.

The rule is simple: inside a layer, raw IDs are fine. Across layers, use selectors.

## One global sequence

Every event Graph Harness writes gets one increasing sequence number, called `seq`.

This gives the whole system a shared clock.

When Graph Harness says "consistent at seq 81234," it means every layer read is being interpreted at the same point in the event log. That makes validation, replay, and debugging much easier because there is one ordered history instead of several unrelated histories.

For example, if a validation run is pinned at `seq 81234`, re-running the same validation against the same diff and the same sequence should produce the same findings.

## Current reads and snapshot reads

Graph Harness supports two kinds of reads.

| Read mode | Use it when | Tradeoff |
|---|---|---|
| Current | You are exploring interactively and want the latest known state | Some layers may be fresher than others |
| Snapshot at `seq N` | You need reproducible answers, such as CI validation or benchmarks | Layers must support reading as of that sequence |

Current reads are useful while a developer is working. If the editor has newer facts than the committed index, Graph Harness can still show what it knows, while marking the freshness clearly.

Snapshot reads are stricter. They are used when the answer must be repeatable. Hard enforcement, such as blocking a pull request, should only depend on layers that can read at the pinned sequence.

## Provenance: every answer says where it came from

Graph Harness should not return a bare answer without saying why it believes that answer.

When a query combines facts from multiple layers, the response includes provenance. Provenance describes things like:

- Which sources contributed to the answer.
- How fresh those sources were.
- How confident the system is.
- Which sequence numbers were involved.

If one part of an answer came from live LSP data and another part came from a possibly stale overlay rule, the consumer should see that. A developer, CI job, or agent should not have to guess whether the answer is safe to rely on.

## `code.core` uses three sources

The `code.core` layer is where Graph Harness builds the normal language-level graph of the codebase. It does not rely on only one source.

It combines three inputs:

| Source | Strength |
|---|---|
| LSP | Good for live editor state and open files |
| SCIP | Good for precise indexed facts from committed code. SCIP is a standard code index format. |
| tree-sitter | Good for fast structural parsing and fallback extraction |

When the sources agree, Graph Harness can merge their facts with stronger provenance. When they disagree, Graph Harness does not hide the disagreement. It records the conflict so a resolver can inspect it.

That prevents a bad situation where an editor-time assistant believes one thing, but CI later validates against a different graph and rejects the change for reasons the assistant never saw.

## What this means during validation

When a diff is validated, Graph Harness can use the layered graph to answer questions like:

- Which entities did this diff directly touch?
- Which routes, queues, schemas, or flows are impacted by those touches?
- Which authored invariants or controls apply to those impacted areas?
- Are any required tests, contract updates, or review steps missing?
- Is the evidence fresh enough to block the change, or should the result be advisory?

The validation pipeline pins a sequence after refreshing the relevant facts. From that point, the remaining checks read from the same snapshot. That is what makes the result reproducible instead of depending on whatever happened to update during the run.

## Replay and history

Because events share one sequence, Graph Harness can replay old states. That means it can reconstruct why a validation run failed in the past, follow how a selector moved after a rename, or rebuild compacted layer state from declared replay inputs.

This is useful for debugging, audits, and benchmarks. The goal is not just to know what the repo looks like now. The goal is to understand how the repo's graph changed over time.

## Short version

The layered graph is Graph Harness's way of keeping repo knowledge organized and trustworthy.

- Different kinds of facts live in different layers.
- Layers are coordinated by one kernel and one global sequence.
- Cross-layer references use selectors, not raw IDs.
- Answers include provenance so consumers know how much to trust them.
- Snapshot reads make validation and replay reproducible.
- Review rules prevent untrusted claims from silently becoming trusted facts.
