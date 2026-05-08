// Package review_queue implements the review.queue layer: the proposal
// state machine that mediates writes from non-trusted sources (agents,
// importers) into authoritative layers.
//
// Full v1 state set (SPEC §10.1):
//
//	pending → accepted / accepted_with_edits / rejected / deferred /
//	          needs_evidence / resolved (+ edit_requested)
//
// Phase 1 ships:
//
//	submitted | needs_evidence | pending_review | accepted | rejected | promoted
//
// `needs_evidence` is reached automatically on Submit when the per-kind
// evidence requirements declared in layer manifests (SPEC §10.4) are not
// satisfied; AddEvidence promotes back to `pending_review` once the gaps
// are filled. Promotion mode is `human_only` for every layer in P1.
// Conflict-as-event covers selector-overlap; invariant-contradiction lands
// in P3.
//
// Trust-policy enforcement: writes from non-`direct_writes_from` sources
// auto-convert to Proposals (SPEC §5.4 + §10.2).
//
// SPEC: §10 (review queue lifecycle), §5.4 (trust policy), §10.4 (evidence
// requirements per proposal type).
package review_queue
