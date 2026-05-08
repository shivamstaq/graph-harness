// Package tui implements the terminal cockpit (charmbracelet/bubbletea).
//
// Views (read-only, navigable with tab/shift-tab):
//
//  1. Workspace status   — root, last_seq, overlay decl count, daemon status
//  2. Findings           — change.process findings (hydrated when validate
//     is run from the TUI; otherwise an empty list)
//  3. Conflicts (P1.I)   — live SymbolDisambiguation events streamed from
//     the daemon's conflicts.list — one row per claim
//     with the source(s) reporting it.
//
// Ctrl-C / q exit cleanly. Full lifecycle (review queue accept/reject/defer,
// finding suppress, plan adherence overlay) lands in P4.
//
// SPEC: §9.5 (TUI), §11.3 (authoring UX — TUI is read-mostly).
package tui
