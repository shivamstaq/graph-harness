# Consistency across surfaces

Graph Harness has three first-class consumers: agents, humans, and CI. They read the same graph-backed source of truth and submit changes through the same review path. Agent integration is one surface of the system, not the product boundary.

The core invariant is simple: when two consumers read the same brief at the same sequence, the underlying facts cannot disagree. The rendering may differ by audience, but the source is the same.

## One brief, multiple surfaces

When a harness attaches during a lifecycle phase, the kernel composes a brief. A brief is not a separate domain entity. It is a view over existing graph state: selector resolution, attached skills, invariants, controls, active findings, repair instructions, lifecycle phase, wake reason, and provenance.

The same brief can be rendered for different consumers.

| Consumer | Surface | Output shape |
|---|---|---|
| Agent | MCP tools and lifecycle hooks | Structured tool responses with scoped context and actions |
| Human | PR comments, IDE views, TUI panels, Studio cards | Markdown and structured UI summaries |
| CI | CLI gates, check runs, machine-readable reports | Exit code plus JSON findings |

This avoids parallel policy channels. The rule an agent sees before editing is the same rule CI applies during validation and the same rule a reviewer sees in the pull request.

## Agents

Agents consume Graph Harness through a curated integration surface, such as MCP tools or lifecycle hooks. The agent does not receive unbounded repository instructions. It receives a scoped brief for the region and phase it is operating in.

For example, before editing an event payload, an agent can receive the relevant boundary context, invariants, required checks, known consumers, and repair recipes. After editing, the same substrate can return incremental findings and remediation instructions.

Agent writes are not special-cased as trusted. An agent can propose overlay changes, anchor updates, rules, or repair improvements, but those claims route through the same trust pipeline as any other untrusted source.

## Humans

Human-facing renderers prioritize reviewability. They should answer what changed, why a harness attached, which invariants or controls fired, what evidence supports the finding, and what action is expected from the reviewer.

Examples of human surfaces include:

- PR comments summarizing blocking and advisory findings.
- IDE code lenses showing relevant harness context near a symbol or file.
- TUI panels for local validation and layer status.
- Studio views for proposal review, selector drift, and conflict resolution.

Human output should remain tied to structured data. A Markdown PR comment is a rendering of findings and provenance, not an independent policy document.

## CI

CI consumes the same substrate through deterministic gates. It needs stable exit codes, structured findings, reproducible validation, and clear blocking semantics.

CI validation should run against a pinned sequence. If a required layer cannot provide snapshot reads, findings that depend on that layer are non-reproducible and cannot be used for hard enforcement. The output still records provenance so teams can distinguish blocking failures from advisory or stale-context findings.

Typical CI behavior includes:

- Run `validate-diff` against the pull request diff.
- Pin `validation_seq` after bounded graph refresh.
- Emit JSON findings for check annotations or downstream tooling.
- Fail only on configured severities or hard controls.
- Preserve enough evidence to reproduce the decision later.

## Symmetric authoring

The three consumers can also write back into the system:

- An agent proposes a new invariant, selector update, or repair recipe.
- A human edits a `.gh` harness or reviews a proposal.
- CI imports a rule pack or emits validation findings.

The kernel applies the layer's trust policy at write time. Direct writes are accepted only from sources allowed by the layer manifest. Other writes become proposals in `review.queue`, with schema validation, selector resolution, evidence checks, and conflict detection before promotion.

This symmetry matters operationally. There is no separate agent memory, CI rule store, or human-only policy channel to reconcile. The system has one reviewed semantic overlay and one proposal lifecycle.

## Sequence consistency

Briefs are composed at a sequence. The same query at the same sequence should produce the same underlying result, independent of renderer. That property is what allows a reviewer to trust that the PR comment, CI gate, and agent context refer to the same graph state.

When current reads are used for interactive exploration, freshness and provenance are explicit. When hard enforcement is required, snapshot reads provide reproducibility.

## Product boundary

Graph Harness does not write application code. It supplies graph-scoped context, constraints, checks, and repair instructions to the actor that is operating: an agent, a human, or CI. Treating all three as first-class consumers keeps the architecture broader than an agent harness while still making agent workflows safer and more reliable.
