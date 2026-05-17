package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	_ "modernc.org/sqlite" // SQLite driver

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/extract"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/semantic_overlay"
)

// activeWorkspace returns the discovered workspace (must be initialized) or
// an error explaining that `init` needs to run first.
func activeWorkspace() (*daemon.Workspace, error) {
	ws, err := daemon.Discover()
	if err != nil {
		return nil, err
	}
	if !ws.IsInitialized() {
		return nil, errors.New("no .graph-harness workspace found in or above cwd; run `graph-harness init` first")
	}
	return ws, nil
}

// openCodeStore opens (or creates) the code.core SQLite store for the
// workspace. The store is colocated with the event log under the runtime dir.
func openCodeStore(ws *daemon.Workspace) (*code_core.Store, *sql.DB, error) {
	dsn := ws.EventLog + ".code.core?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	store, err := code_core.NewStore(db)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return store, db, nil
}

// indexWorkspaceCode runs the source.live → code.core ingestion path
// through extract.Orchestrator so every available source feeds
// code_core.Unifier per SPEC §6.11. Tree-sitter, SCIP, and LSP are all
// enabled by default; the orchestrator's detector + lazy-spawn +
// bounded-timeout machinery (SPEC §6.18) handles missing tools
// gracefully — a missing LSP server emits a code.core.ExtractorUnavailable
// event and the indexer carries on with the remaining sources. Set
// GRAPH_HARNESS_DISABLE_LSP=1 (or pass --no-lsp) to suppress LSP for a
// shell session or single invocation.
//
// Build-order tolerance: when SCIP indexes are absent or LSP is
// disabled, the orchestrator degrades to tree-sitter-only ingestion —
// single-source provenance is still produced and downstream paths
// keep working (SPEC §6.11).
//
// Idempotent: re-running is cheap because identity is
// content-addressable; the unifier upserts provenance per-source.
//
// Retained for any future caller that doesn't wire the
// addExtractorToggleFlags / extractOptionsFromFlags helpers; current
// callers all pass extract.Options explicitly via WithOptions.
//
//nolint:unused // see comment above
func indexWorkspaceCode(ctx context.Context, ws *daemon.Workspace, store *code_core.Store, log *facts.EventLog) error {
	return indexWorkspaceCodeWithOptions(ctx, ws, store, log, extract.Options{
		DisableLSP:  lspDisabledFromEnv(),
		DisableSCIP: false,
	})
}

// lspDisabledFromEnv resolves the LSP enable state from environment
// variables. Default is enabled (per SPEC §6.18); an explicit
// GRAPH_HARNESS_DISABLE_LSP=1 disables it. The legacy
// GRAPH_HARNESS_ENABLE_LSP is honored for one minor version with a
// deprecation note in the docs — when set to "0" it disables LSP, when
// unset it leaves the default in place.
func lspDisabledFromEnv() bool {
	if os.Getenv("GRAPH_HARNESS_DISABLE_LSP") == "1" {
		return true
	}
	// Backwards-compat: the old enable-flag is still honored when
	// explicitly set to "0", which used to mean "force off". Setting it
	// to "1" matches the new default and is now redundant.
	if v := os.Getenv("GRAPH_HARNESS_ENABLE_LSP"); v == "0" {
		return true
	}
	return false
}

// indexWorkspaceCodeWithOptions is the option-taking variant used by
// CLI commands that wire addExtractorToggleFlags so the user can
// flip --no-lsp / --no-scip / --no-treesitter for a single
// invocation. Plain indexWorkspaceCode keeps the production-default
// shape for callers that don't expose the toggles.
func indexWorkspaceCodeWithOptions(ctx context.Context, ws *daemon.Workspace, store *code_core.Store, log *facts.EventLog, opts extract.Options) error {
	unifier := &code_core.Unifier{
		Store:   store,
		Emitter: codeCoreEventEmitter(log),
	}
	orch, err := extract.NewOrchestrator(ws.Root, store, unifier, opts)
	if err != nil {
		return err
	}
	defer func() {
		// Bound shutdown so a hung LSP server doesn't deadlock the CLI.
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = orch.Close(shutCtx)
	}()
	seq := uint64(0)
	if log != nil {
		seq = log.LastSeq()
	}
	return orch.IndexAll(ctx, seq)
}

// codeCoreEventEmitter adapts the kernel facts.EventLog to the
// code_core.EventEmitter contract so the unifier can publish
// SymbolDisambiguation events without taking a kernel/facts dep.
// Returns nil when log is nil (CLI calls that don't open the event
// log — none today, but defensive against future use sites).
func codeCoreEventEmitter(log *facts.EventLog) code_core.EventEmitter {
	if log == nil {
		return nil
	}
	return code_core.EventEmitterFunc(func(ctx context.Context, kind string, payload []byte) error {
		_, err := log.Append(ctx, []kernel.Event{{
			Layer:      "code.core",
			Kind:       kind,
			Payload:    json.RawMessage(payload),
			ProducedBy: kernel.SourceClass("layer:code.core"),
		}})
		return err
	})
}

// openIndexedStore is the writable batch-path opener for read
// commands that still need to run the orchestrator once before
// answering (`code list --batch`, `selectors test --batch`). It
// opens the event log + code.core store writable, runs the
// orchestrator over the workspace once, and returns both handles
// plus a single close function the caller defers.
//
// Strictly speaking the daemon owns the writer-monopoly post-
// P0.5.T18 — but `--batch` runs without the daemon, so there is no
// concurrent writer to fight. The pattern works under the SPEC §9.11
// carve-out: batch mode is daemon-free; the operator opts into
// running the orchestrator inline.
func openIndexedStore(
	ctx context.Context,
	ws *daemon.Workspace,
	cmd *cobra.Command,
) (*facts.EventLog, *sql.DB, *code_core.Store, func(), error) {
	log, err := facts.OpenEventLog(ws.EventLog)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	store, db, err := openCodeStore(ws)
	if err != nil {
		_ = log.Close()
		return nil, nil, nil, nil, err
	}
	if err := indexWorkspaceCodeWithOptions(ctx, ws, store, log, extractOptionsFromFlags(cmd)); err != nil {
		_ = db.Close()
		_ = log.Close()
		return nil, nil, nil, nil, err
	}
	closeAll := func() {
		_ = db.Close()
		_ = log.Close()
	}
	return log, db, store, closeAll, nil
}

// loadOverlay reads .graph-harness/overlay/**/*.gh into an Overlay.
func loadOverlay(ws *daemon.Workspace) (*semantic_overlay.Overlay, error) {
	o := semantic_overlay.NewOverlay()
	errs, err := o.Load(ws.OverlayDir)
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		// Surface the first parse error to the user but return the partial
		// overlay so they can see what *did* parse.
		for path, e := range errs {
			return o, fmt.Errorf("%s: %w", path, e)
		}
	}
	return o, nil
}
