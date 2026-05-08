// Package mcp implements the MCP (Model Context Protocol) adapter that
// exposes Graph Harness capabilities to coding agents. The adapter is a
// curated, opinionated tool surface — not a thin pass-through (SPEC §9.3).
//
// Tools (post P0 bring-up + P1 additions):
//
//	explore        — bounded graph navigation around a selector
//	validate_diff  — run change.process on a candidate diff
//	get_context    — fetch flow / invariant / control evidence for a selector
//	query          — parse a DSL query and return its canonical AST envelope
//	before_edit    — P1: snapshot selector resolution + body hashes pre-edit
//	after_edit     — P1: validate a unified diff against the pre-edit snapshot
//
// Resources:
//
//	gh://workspace/status                       — workspace + last_seq summary
//	gh://overlay/<rel.gh>                       — read a .gh source file
//	gh://entity/code.core/<kind>/<id>           — P1: merged provenance for an entity
//
// Prompts and notifications are scaffolded with one example each.
//
// Transport: stdio (line-delimited JSON-RPC 2.0 over the agent's stdin/stdout).
// HTTP/SSE deferred. All tool calls run trust-policy enforcement: writes
// from agents convert to Proposals via the review queue.
//
// SPEC: §9.3 (MCP adapter), §10.2 (promotion modes — agents are non-trusted
// by default).
package mcp
