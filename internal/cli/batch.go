// Package cli — batch-mode opening of layer SQLite stores in
// read-only mode (P0.5.T16 / SPEC §9.11).
//
// Batch mode is the deterministic, daemon-free path for one-shot
// read CLI invocations (`graph-harness selectors test X --batch`,
// `graph-harness validate-diff --batch --diff <file>`, etc.).
// The mode:
//
//   - Opens layer SQLite stores with `?mode=ro&_pragma=query_only(1)`
//     so accidental writes fail loudly.
//   - Pins to the last-committed kernel seq at startup; subsequent
//     reads within the same invocation see the same head, so the
//     same diff at the same seq deterministically produces the same
//     output (the property P0.5.T17 gates).
//   - Builds a kernel.Router with the same layer adapters the daemon
//     installs, so batch reads lower through the same Kernel.Route
//     code path as daemon reads — guaranteeing byte-identical output
//     (modulo daemon-emitted side-effects).
//
// Caller must defer BatchHandle.Close to release the SQLite handles.
package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "modernc.org/sqlite" // SQLite driver

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/semantic_overlay"
)

// BatchHandle holds the per-invocation layer handles opened in
// read-only mode + the pinned-head seq sampled at open time.
type BatchHandle struct {
	// PinnedSeq is the last-committed kernel seq sampled at open
	// time. All reads in this batch invocation are reported as
	// resolved_at_kernel_seq = PinnedSeq.
	PinnedSeq uint64

	// EventLog is the read-only kernel event log handle. Closing
	// the underlying DB is handled via Close.
	EventLog *facts.EventLog

	// Code is the code.core store opened in read-only mode.
	Code *code_core.Store

	// Overlay is the semantic.overlay loaded from disk. Pure
	// in-memory after Load; no SQLite handle.
	Overlay *semantic_overlay.Overlay

	codeDB *sql.DB
}

// OpenBatch opens every layer SQLite store the read-side CLI surface
// needs in read-only mode, samples the kernel head, and returns the
// pinned handle. The handles are independent of any running daemon —
// the SQLite WAL journal mode lets readers operate concurrently with
// the daemon's writer without blocking each other (SPEC §9.11).
func OpenBatch(_ context.Context, ws *daemon.Workspace) (*BatchHandle, error) {
	if ws == nil {
		return nil, errors.New("batch: workspace required")
	}
	// Event log opens in WAL mode; readers concurrent with the daemon
	// writer are safe. We sample LastSeq and call that our pinned head.
	log, err := facts.OpenEventLog(ws.EventLog)
	if err != nil {
		return nil, fmt.Errorf("open event log: %w", err)
	}
	pinned := log.LastSeq()

	// code.core store opens in read-only mode. The query_only pragma
	// is set per-connection by the modernc.org/sqlite driver via the
	// `_pragma=query_only(1)` DSN parameter; combined with mode=ro it
	// surfaces accidental writes as a clear error.
	codeDSN := ws.EventLog + ".code.core?mode=ro&_pragma=journal_mode(WAL)&_pragma=query_only(1)"
	codeDB, err := sql.Open("sqlite", codeDSN)
	if err != nil {
		_ = log.Close()
		return nil, fmt.Errorf("open code.core (ro): %w", err)
	}
	if err := codeDB.Ping(); err != nil {
		_ = codeDB.Close()
		_ = log.Close()
		return nil, fmt.Errorf("ping code.core (ro): %w", err)
	}
	store, err := code_core.NewStore(codeDB)
	if err != nil {
		_ = codeDB.Close()
		_ = log.Close()
		return nil, fmt.Errorf("init code.core store: %w", err)
	}

	// semantic.overlay is filesystem-backed (.gh files under
	// .graph-harness/overlay/). Load in-memory; the snapshot is
	// inherently read-only.
	overlay := semantic_overlay.NewOverlay()
	if _, err := overlay.Load(ws.OverlayDir); err != nil {
		_ = codeDB.Close()
		_ = log.Close()
		return nil, fmt.Errorf("load overlay: %w", err)
	}

	return &BatchHandle{
		PinnedSeq: pinned,
		EventLog:  log,
		Code:      store,
		Overlay:   overlay,
		codeDB:    codeDB,
	}, nil
}

// Close releases the SQLite handles. Always safe to call.
func (b *BatchHandle) Close() error {
	var firstErr error
	if b.codeDB != nil {
		if err := b.codeDB.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if b.EventLog != nil {
		if err := b.EventLog.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
