// Package extract orchestrates source.live extraction across every
// available fact source (LSP, SCIP, tree-sitter) and routes the
// resulting Symbol streams through code_core.Unifier per SPEC §6.11.
//
// This package exists to break the would-be cycle between
// internal/source_live (Symbol producer) and internal/code_core
// (Symbol consumer + Unifier). It depends on both, neither depends
// on it, and CLI / daemon code calls Orchestrator.IndexAll for the
// initial workspace sweep + Orchestrator.IndexFile for incremental
// fsnotify updates.
//
// Build-order tolerance (SPEC §6.11): when LSP servers aren't on
// $PATH or no .scip-index/ files exist, the orchestrator degrades to
// tree-sitter-only ingestion without warnings — single-source
// provenance is still produced and the rest of the pipeline keeps
// working.
package extract
