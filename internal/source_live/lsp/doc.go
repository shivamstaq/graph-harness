// Package lsp implements the LSP-side fact source for source.live.
//
// It hosts a small set of language-server subprocesses (gopls,
// typescript-language-server, pyright-langserver) and translates the
// subset of LSP we need (textDocument/documentSymbol, definition,
// references) into source_live.Symbol envelopes for downstream
// unification in code.core.
//
// Design (see plan §1.A and SPEC §6.11):
//
//   - host.go owns the subprocess supervisor: lazy start, health-check,
//     restart-on-crash, structured shutdown. One Host serves the whole
//     workspace; drivers are spawned on first use of their language.
//   - driver.go defines the cross-language Driver interface that
//     gopls.go / tsserver.go / pyright.go implement. Concrete drivers
//     only encode language-specific quirks (executable name, init
//     options, capability detection).
//   - registry.go discovers available drivers via $PATH and returns
//     only the languages whose servers are actually installed —
//     graceful degradation when a tool is missing.
//   - jsonrpc.go is the lightweight JSON-RPC 2.0 transport over a
//     subprocess's stdin/stdout. Content-Length framed per the LSP
//     base protocol. We don't pull go.lsp.dev/jsonrpc2 because the
//     subset we use is small and the message types are stable.
//
// LSP servers go through three lifecycle states:
//
//	NotStarted → Initializing → Ready (with health pings)
//
// On a process crash, Host transitions back to NotStarted and the next
// request lazily re-spawns. Pyright in particular has a multi-second
// cold start; we hide that latency behind the lazy-spawn path so
// non-Python workspaces never pay it.
package lsp
