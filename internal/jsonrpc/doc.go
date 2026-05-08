// Package jsonrpc hosts the daemon's JSON-RPC 2.0 server (sourcegraph/jsonrpc2)
// over a Unix domain socket on Linux/macOS and a named pipe on Windows. Method
// dispatch is capability-gated: only methods declared by an installed layer's
// manifest are reachable.
//
// All consumer surfaces — CLI, TUI, Studio, VS Code, MCP adapter — speak the
// same JSON-RPC surface to the daemon. There is no second protocol.
//
// SPEC: §6.16 (event bus mechanics — filter expressions share the DSL parser),
// §9.1 (daemon lifecycle), §9.9 (consumer surface summary).
//
// Methods registered (P0 baseline + P1 extensions):
//
//	rpc.discover                  — list available methods + capability tags
//	daemon.ping                   — round-trip health probe
//	daemon.shutdown               — request graceful daemon stop
//	status                        — workspace + last_seq + counts summary
//	layers.list                   — installed layers + state
//	selectors.test                — resolve a named selector → envelope
//	selectors.preview             — resolve a literal anchor against code.core
//	flows.list                    — flows in the overlay
//	query.parse                   — parse a DSL string → AST envelope
//	validate.diff                 — run change.process pipeline against unified diff
//	review.list / review.get      — read review queue
//	review.accept / review.reject — state transitions
//	overlay.save                  — write a .gh file under <workspace>/.graph-harness/overlay/
//	mcp.before_edit               — P1: pre-edit selector preview snapshot
//	mcp.after_edit                — P1: post-edit validate-diff hand-off
//	conflicts.list                — P1: live SymbolDisambiguation events
//
// Capability gating: a method is reachable only when the union of capabilities
// declared by the kernel manifest registry contains the method's required
// capability tag. A request for an ungated method returns JSON-RPC error
// -32601 (method not found).
package jsonrpc
