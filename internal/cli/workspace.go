package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	_ "modernc.org/sqlite" // SQLite driver

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
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
	if err := orch.IndexAll(ctx, seq); err != nil {
		return err
	}
	// After code.core indexing, run the framework extractors once so
	// code.framework entities (Route / EventPublisher / SchemaField /
	// …) materialize in the SAME in-process index the batch CLI reads.
	// Without this, `code list --batch` / `validate-diff --batch` would
	// only ever see code.core rows — the framework Dispatcher otherwise
	// runs only inside `daemon serve`. Best-effort: a framework
	// extraction error must not fail the code.core listing.
	if log != nil {
		_ = runFrameworkExtractorsOnce(ctx, ws, store, log)
	}
	return nil
}

// runFrameworkExtractorsOnce builds a one-shot code.framework
// Dispatcher, runs CatchUp over every File entity currently in the
// store, and tears it down. This is the batch-mode analogue of the
// long-lived Dispatcher the daemon-serve loop owns — it makes
// framework entities available to one-shot CLI reads without a
// running daemon. The Writer projects each emission into a queryable
// code_core.Entity row; the Dispatcher's compare-before-emit makes
// re-runs idempotent.
func runFrameworkExtractorsOnce(ctx context.Context, ws *daemon.Workspace, store *code_core.Store, log *facts.EventLog) error {
	cfg, _ := code_framework.LoadConfig(ws.Root)
	disp, err := code_framework.NewDispatcher(code_framework.DispatcherConfig{
		Workspace: ws.Root,
		Facts:     facts.NewEventLogFacts(log, "code.framework"),
		EventLog:  log,
		Writer:    code_framework.NewEntityWriter(store),
		Config:    cfg,
	})
	if err != nil {
		return err
	}
	if err := disp.Start(ctx); err != nil {
		return err
	}
	defer func() { _ = disp.Stop(context.Background()) }()
	paths, err := frameworkCatchUpPaths(ctx, ws, store)
	if err != nil {
		return err
	}
	return catchUpStable(ctx, disp, paths)
}

// catchUpStable runs the dispatcher CatchUp twice. The first pass
// materializes producer entities (events, routes, schemas); the second
// lets cross-referencing extractors (the test extractor's ContractTest
// linker reads existing Event/Route rows via LoadKnownRows) bind to
// producers that a later-ordered file declared. Compare-before-emit
// makes the second pass a no-op for already-materialized entities, so
// the only new rows are the cross-references. Two passes suffice — the
// dependency depth is one (tests → producers); there are no
// test-references-test chains in the v1 extractor set.
func catchUpStable(ctx context.Context, disp *code_framework.Dispatcher, paths []string) error {
	if err := disp.CatchUp(ctx, paths); err != nil {
		return err
	}
	return disp.CatchUp(ctx, paths)
}

// frameworkCatchUpPaths returns the workspace-relative file paths the
// framework Dispatcher's CatchUp should replay. It unions:
//
//   - every code.core File entity (the indexed source files: Go / TS /
//     Python — these carry the publisher/handler/test functions); and
//   - non-source framework files the code.core indexer does NOT track
//     (schema.prisma, *.sql, *.proto, migration scripts, *.graphql) —
//     without these the schema / migration / graphql / generated
//     extractors would never receive a FileChanged for the files they
//     parse.
//
// Source-file ordering (code.core entities first) is preserved so the
// contract-test linker sees the Event/Route rows a sibling source file
// produced before its own file is processed.
func frameworkCatchUpPaths(ctx context.Context, ws *daemon.Workspace, store *code_core.Store) ([]string, error) {
	var paths []string
	seen := map[string]struct{}{}
	add := func(rel string) {
		if rel == "" {
			return
		}
		if _, dup := seen[rel]; dup {
			return
		}
		seen[rel] = struct{}{}
		paths = append(paths, rel)
	}

	// 1. Indexed source files (preserve store order).
	files, err := store.ListEntities(ctx, code_core.ListFilter{})
	if err != nil {
		return nil, err
	}
	for _, e := range files {
		if e.Kind == code_core.KindFile {
			add(e.Path)
		}
	}

	// 2. Non-source framework files via a bounded filesystem walk.
	_ = filepath.WalkDir(ws.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip unreadable entries, never abort the walk
		}
		if d.IsDir() {
			if frameworkWalkSkipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !frameworkNonSourceFile(d.Name()) {
			return nil
		}
		rel, rerr := filepath.Rel(ws.Root, p)
		if rerr != nil {
			return nil
		}
		add(filepath.ToSlash(rel))
		return nil
	})
	return paths, nil
}

// frameworkWalkSkipDir lists directory names the framework file walk
// skips wholesale — VCS metadata, the workspace's own state dir, and
// dependency/build output trees that never hold first-party framework
// declarations.
func frameworkWalkSkipDir(name string) bool {
	switch name {
	case ".git", ".graph-harness", ".scip-index",
		"node_modules", "vendor", "dist", "build", ".next", "target",
		"__pycache__", ".venv", "venv", ".idea", ".vscode":
		return true
	}
	return false
}

// frameworkNonSourceFile reports whether name is a non-source file a
// framework extractor parses directly (the code.core indexer only
// tracks .go/.ts/.py, so these would otherwise never reach an
// extractor). Source files are covered by the code.core File-entity
// pass in frameworkCatchUpPaths.
func frameworkNonSourceFile(name string) bool {
	if name == "schema.prisma" {
		return true
	}
	switch ext := strings.ToLower(filepath.Ext(name)); ext {
	case ".prisma", ".sql", ".proto", ".graphql", ".graphqls":
		return true
	}
	return false
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
