// Package change_process implements the change.process layer: the per-change
// validation pipeline that produces ValidationFinding and RepairInstruction
// events. The pipeline is twelve stages (SPEC §8.1), idempotent at a fixed
// kernel sequence.
//
// Phase 1 stage status (plan §1, §P1.G):
//
//  1. parse diff                         FULL
//  2. map hunks → code.core entities     FULL
//  3. bounded refresh of source.live     WORKING SUBSET
//     3b. pin validation_seq + dirty/stale  WORKING SUBSET
//  4. compute touched_set                FULL
//  5. compute impacted_set (BFS only)    FULL  (Mangle integration → P3)
//  6. selector resolution + flow match   FULL
//     + emits `symbol_disambiguation`
//     findings from
//     code.core.SymbolDisambiguation
//     events (P1.G)
//  7. invariant check                    PASS-THROUGH (P3)
//  8. control evaluation                 PASS-THROUGH (P3)
//  9. agent / skill hooks                PASS-THROUGH
//  10. emit ValidationFinding events     FULL — kinds:
//     flow_unreviewed,
//     unresolved_anchor (P1.G),
//     symbol_disambiguation (P1.G),
//     selector_reanchored (gate crit. 6)
//  11. RepairInstruction synthesis       PASS-THROUGH (P3)
//  12. emit ValidateDiffResult summary   FULL
//
// Pass-through stages are working subsets, not placeholders: they advance
// the pipeline correctly even when emitting empty output. Same idempotence
// guarantee applies.
//
// SPEC: §8 (validation pipeline), §6.14 (traversal — used in stage 5).
package change_process
