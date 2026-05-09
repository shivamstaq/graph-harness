# Lifecycle attachment

Lifecycle attachment is the mechanism that brings relevant harness content into an operation at the moment it is needed. When an agent prepares an edit, a human opens a file, or CI validates a diff, the kernel resolves the affected graph region and attaches the matching context, rules, tools, checks, and repair recipes.

The repository substrate already contains the facts. Attachment selects the relevant slice for a specific operation and renders it for the current consumer.

## Harness scope

A harness is a `.gh` artifact that pins operational guidance to a region of the repository graph. It can carry context, invariants, external tools, verification actions, and repair recipes. The selector in the harness defines the region; lifecycle phases define when each behavior applies.

Harnesses are not global instruction blobs. They attach only when their selector overlaps the operation's touched or impacted region. This keeps guidance specific to the code being changed and prevents unrelated rules from becoming ambient background noise.

## Phases

Graph Harness recognizes six lifecycle phases.

| Phase | When it runs | Typical attachment |
|---|---|---|
| `explore` | An agent or human asks for context before deciding what to do | Region summary, relevant skills, informational invariants, related flows |
| `before_edit` | An edit is about to begin | Required context, active invariants, acknowledgements, expected checks |
| `after_edit` | A change has landed locally but is not yet final | Incremental findings, touched regions, repair previews |
| `validate_diff` | Pre-commit, pre-push, local validation, or CI validation runs against a diff | Structured findings, invariant results, required checks, repair instructions |
| `on_pr` | A pull request is opened or updated | PR-ready findings, check status, reviewer-facing summaries, policy-required actions |
| `before_done` | An agent or human marks the task complete | Outstanding findings, unresolved proposals, cross-harness consistency checks |

A harness does not need to implement every phase. A policy-focused harness may only participate in `validate_diff` and `on_pr`. A context-heavy harness for agent workflows may attach during `explore` and `before_edit` as well.

## Activation model

Attachment is driven by graph impact, not just direct file edits.

The validation pipeline first maps the operation to a touched set: entities directly modified by the diff or selected by the current operation. It then expands that set into an impacted set using relation-aware traversal. Impact propagation can follow call edges, schema reads and writes, event publishers and consumers, generated-artifact relationships, dependency edges, and framework-specific relations.

This distinction is central to the product:

- A payload schema may be touched directly.
- Consumers of that payload may be impacted through `consumes_event` edges.
- Generated files may be impacted when their generator changes.
- A caller may be impacted when a callee's behavior changes.

Harnesses attach when their selector overlaps the impacted set. The attachment includes a reason, such as `direct_touch` or `impacted_via`, so consumers can distinguish code that changed directly from code reached through the impact graph.

## Blast radius

Relation-aware traversal is mathematically bounded to prevent a single schema change from waking up the entire repository. This bounded zone is the **blast radius**.

When a central entity (like an `EventPayload` struct) is modified, the impact graph expands outward along specific relationship edges, such as `consumes_event`, `reads_schema`, or `calls_function`. The blast radius wraps the touched entity and its immediate impacted set. Harnesses attach exclusively to nodes inside this boundary, ensuring that operations in one part of the codebase do not trigger ambient background noise or unrelated policy checks in unimpacted regions.

## Phase-specific content

The same harness can render different content at different phases.

During `explore`, the brief emphasizes background context and relevant architectural boundaries. During `before_edit`, it emphasizes constraints that should shape the edit. During `validate_diff` and `on_pr`, it emphasizes evidence: findings, failed checks, invariant violations, and repair instructions. During `before_done`, it emphasizes unresolved work and consistency across all activated harnesses.

This avoids treating every operation as a validation event. An agent exploring a region needs context; CI deciding whether to block a merge needs evidence and reproducible results.

## Outputs

Attachment is not read-only. Operations can contribute new facts back into the graph.

Examples include:

- A plan recorded as a `change.process` artifact.
- A diff mapped to `TouchedRegion` entities.
- A failed invariant emitted as a `ValidationFinding`.
- A generated remediation emitted as a `RepairInstruction`.
- An agent-suggested rule routed as a proposal in `review.queue`.
- A drift detector's anchor update routed through the trust pipeline.

These outputs are appended to the same substrate that future operations read from. Reviewed and promoted changes become part of the repository's durable semantic overlay; transient process artifacts remain scoped to the change that produced them.

## Consistency

Attachment respects the same consistency model as the layered graph. Interactive phases can read current state and report freshness. Enforcement phases pin a sequence and read from that snapshot. This prevents a validation run from mixing pre-refresh and post-refresh facts across layers.

For hard controls, phase output must carry provenance. If a harness attaches because of stale or unresolved selector evidence, the consumer should see that before treating the result as blocking.
