// Package daemon owns the workspace daemon lifecycle: watchman-style
// lazy-spawn on first CLI invocation, pidfile + socket management under the
// platform runtime directory, and idle-timeout (default 1h, configurable).
//
// Platform paths:
//
//	Linux:   $XDG_RUNTIME_DIR/graph-harness/<workspace-hash>.sock
//	macOS:   ~/Library/Application Support/graph-harness/<workspace-hash>.sock
//	Windows: \\.\pipe\graph-harness-<workspace-hash>
//
// One workspace = one daemon = one shared graph cache. CI integration works
// through the same daemon path; a `--no-daemon` mode runs single-shot.
//
// Lifecycle (post P0 bring-up):
//
//	EnsureRunning(ctx, ws)        — returns when a daemon is reachable. If
//	                                no daemon is currently bound to the
//	                                workspace socket, spawns the current
//	                                binary as a child with `daemon serve`.
//	                                Idempotent across concurrent CLI calls
//	                                (uses an exclusive flock on the pidfile).
//	Run(ctx, ws, opts)            — block in the listener loop; exit when
//	                                ctx is cancelled, daemon.shutdown is
//	                                requested, or the configured idle
//	                                timeout elapses without RPC activity.
//	IsRunning(ws)                 — fast-path probe: checks pidfile presence
//	                                + signals the recorded PID.
//	Stop(ctx, ws)                 — best-effort daemon.shutdown RPC, falling
//	                                back to SIGTERM after 5s.
//
// SPEC: §9.1 (lazy-spawn), §9.7 (CI integration).
package daemon
