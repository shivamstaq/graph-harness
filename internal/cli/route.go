// Package cli — daemon-canonical routing for interactive commands
// (P0.5.T15 + P0.5.T16 / SPEC §9.1, §9.11).
//
// Interactive read commands (`selectors test`, `code list`, `entity
// provenance`, `flows list`, `validate-diff` in interactive context,
// etc.) should funnel through the daemon over JSON-RPC so the daemon
// stays the canonical source of truth for materialized state — there
// is exactly one writer (P0.5.T18 enforces this at the storage seam),
// and one read seam (Kernel.Route, lowered by every JSON-RPC handler
// per P0.5.T13).
//
// Two carve-outs:
//
//  1. **Batch mode** (SPEC §9.11): `--batch`, `GRAPH_HARNESS_BATCH=1`,
//     or daemon-unavailable-and-read-only opens the layer SQLite stores
//     read-only, pins to the last-committed kernel seq at startup, and
//     produces deterministic output without contacting the daemon.
//     Writes still require the daemon (which auto-spawns via
//     EnsureRunning).
//  2. **Daemon-write commands** that must journal through the daemon's
//     single-writer path (overlay save, review accept/reject, etc.):
//     these auto-EnsureRunning regardless of --batch.
//
// The router below picks the mode, opens the right handles, and
// returns a RouteHandle the command body uses. Callers MUST defer
// Close on the returned handle.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
)

// RouteMode reports whether the command will route through the daemon
// or open local SQLite directly. Batch mode is read-only by contract.
type RouteMode int

// Route modes.
const (
	// ModeDaemon: command routes every call through the JSON-RPC daemon.
	// The daemon is auto-spawned if not already running.
	ModeDaemon RouteMode = iota
	// ModeBatch: command opens layer SQLite stores read-only and pins
	// to the last-committed seq at startup. Read-only by contract.
	ModeBatch
)

// String returns the mode name for logging / debugging.
func (m RouteMode) String() string {
	switch m {
	case ModeDaemon:
		return "daemon"
	case ModeBatch:
		return "batch"
	}
	return "unknown"
}

// RouteHandle is the per-command handle returned by ResolveRoute.
// Exactly one of Client / Local is populated depending on mode.
//
// In ModeDaemon, Client is a connected jsonrpc.Client and Local is
// nil. In ModeBatch, Client is nil and Local holds the read-only
// layer handles + the pinned head seq.
type RouteHandle struct {
	Mode      RouteMode
	Workspace *daemon.Workspace

	// Client is populated in ModeDaemon. Use it to invoke daemon
	// methods. Closed by Close().
	Client *jsonrpc.Client

	// Local is populated in ModeBatch. Holds opened layer handles
	// that the command body reads from directly. Closed by Close().
	Local *BatchHandle
}

// Close releases any resources the handle owns. Always safe to call
// (no-op on partially-populated handles).
func (h *RouteHandle) Close() error {
	var firstErr error
	if h.Client != nil {
		if err := h.Client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if h.Local != nil {
		if err := h.Local.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// RouteOptions controls ResolveRoute's mode selection.
type RouteOptions struct {
	// PreferBatch is set by callers when --batch was passed or the
	// command is intrinsically read-only. ResolveRoute still falls
	// back to daemon when batch is unavailable for that command.
	PreferBatch bool

	// RequireWriter is set by commands that journal through the
	// daemon's single-writer path (overlay save, review accept,
	// validate-diff in write mode). Forces ModeDaemon even when
	// --batch is set; surfaces a clear error if the daemon cannot
	// be reached.
	RequireWriter bool

	// SpawnTimeout caps how long ResolveRoute waits for an auto-
	// spawned daemon to become connectable. Defaults to 15s.
	SpawnTimeout time.Duration
}

// BatchEnvVar names the environment variable that flips the default
// route to batch mode. Set to "1" to enable, anything else (including
// unset) leaves the per-command choice in effect.
const BatchEnvVar = "GRAPH_HARNESS_BATCH"

// batchFromEnv reports whether the env var forces batch mode.
func batchFromEnv() bool { return os.Getenv(BatchEnvVar) == "1" }

// ResolveRoute picks the route mode for an interactive command and
// returns a populated RouteHandle. Caller must defer handle.Close().
//
// Mode selection (SPEC §9.1 + §9.11):
//
//  1. RequireWriter ⇒ ModeDaemon (auto-spawn if needed). Writers
//     must journal through the daemon's single-writer path; there
//     is no read-only fallback.
//  2. PreferBatch || GRAPH_HARNESS_BATCH=1 ⇒ ModeBatch unconditionally.
//  3. Otherwise (read-only command, no explicit batch request):
//     prefer an already-running daemon when one is available; if
//     no daemon is running, degrade to ModeBatch rather than
//     auto-spawning. This is the SPEC §9.11 "daemon-unavailable +
//     read-only command" path — it keeps one-shot CLI invocations
//     deterministic and avoids paying the daemon hydration latency
//     for a single read. Long-lived consumers (TUI, Studio, MCP)
//     stay daemon-canonical because they keep a connection open.
//
// The caller decides via RouteOptions whether spawn-on-read is
// desired (set RequireWriter to force daemon, or set PreferBatch to
// force batch). The default — both flags unset — is the SPEC-aligned
// "use daemon if running, else batch" for reads.
func ResolveRoute(ctx context.Context, ws *daemon.Workspace, opts RouteOptions) (*RouteHandle, error) {
	if ws == nil {
		return nil, errors.New("ResolveRoute: workspace required")
	}
	wantBatch := (opts.PreferBatch || batchFromEnv()) && !opts.RequireWriter
	if wantBatch {
		bh, err := OpenBatch(ctx, ws)
		if err != nil {
			return nil, fmt.Errorf("open batch handles: %w", err)
		}
		return &RouteHandle{Mode: ModeBatch, Workspace: ws, Local: bh}, nil
	}

	// Writers: must journal through the daemon (single-writer
	// monopoly per SPEC §9.1 + P0.5.T18).
	if opts.RequireWriter {
		timeout := opts.SpawnTimeout
		if timeout <= 0 {
			timeout = 15 * time.Second
		}
		spawnCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := daemon.EnsureRunning(spawnCtx, ws); err != nil {
			return nil, fmt.Errorf("ensure daemon running: %w", err)
		}
		client, err := jsonrpc.Dial(ctx, ws.SocketPath)
		if err != nil {
			return nil, fmt.Errorf("dial daemon: %w", err)
		}
		return &RouteHandle{Mode: ModeDaemon, Workspace: ws, Client: client}, nil
	}

	// Read-only command, no explicit batch flag. Prefer daemon if
	// already running (avoids re-indexing in-process); else batch.
	if daemon.IsRunning(ws) {
		client, err := jsonrpc.Dial(ctx, ws.SocketPath)
		if err == nil {
			return &RouteHandle{Mode: ModeDaemon, Workspace: ws, Client: client}, nil
		}
		// dial failed despite running pidfile — fall through.
	}
	bh, err := OpenBatch(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("open batch handles: %w", err)
	}
	return &RouteHandle{Mode: ModeBatch, Workspace: ws, Local: bh}, nil
}
