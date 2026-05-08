package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

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
// code_core.Unifier per SPEC §6.11. Tree-sitter + SCIP are enabled
// unconditionally — both are fast (parse / file-read). LSP is opt-in
// behind GRAPH_HARNESS_ENABLE_LSP=1 because cold-start latency
// (gopls/tsserver/pyright workspace load) makes per-CLI-invocation
// spawning impractical for latency-sensitive paths like
// `selectors test` / `validate-diff`. The daemon's long-lived
// background indexer owns the persistent LSP host (P2 wire-up); CI /
// dev users can opt in with the env var to verify three-source merge
// end-to-end.
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
		DisableLSP:  os.Getenv("GRAPH_HARNESS_ENABLE_LSP") != "1",
		DisableSCIP: false,
	})
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
