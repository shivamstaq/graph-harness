// Package semantic_overlay implements the semantic.overlay layer: the durable,
// human-authored semantics of the system (Flow, Invariant, Control, Skill,
// Selector, Runbook, Risk label, Boundary, Ownership hint).
//
// Source-of-truth is filesystem-canonical .gh files under
// .graph-harness/overlay/**; SQLite is the index. The importer reads .gh,
// the DSL parser produces a JSON AST, and the layer emits OverlayItemImported
// events. This makes the overlay git-tracked, PR-reviewable, and durable.
//
// Selector resolution is the central operation. v1 anchor families are closed
// (SPEC §3.1) with one `graph_query { dsl }` escape hatch. Resolution returns
// a structured envelope (SPEC §3.2) with five outcomes: bound, reanchored,
// ambiguous, unresolved, superseded.
//
// Phase 1 ships the multi-anchor ladder with outcomes {bound, reanchored,
// unresolved}; the `ambiguous` and `superseded` outcomes remain deferred to
// P3. Drift-event subscription invalidates the resolution cache lazily.
//
// SPEC: §2.3, §3 (selectors), §11 (.gh DSL), §4.8 (event log + overlay
// canonicality).
package semantic_overlay
