package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // SQLite driver registered for the daemon path

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/extract"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/review_queue"
	"github.com/shivamstaq/graph-harness/internal/semantic_overlay"
	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// Resources bundles the long-lived kernel handles a daemon (or a single-shot
// CLI in --no-daemon mode) needs. Close releases everything.
type Resources struct {
	Workspace *Workspace
	Log       *facts.EventLog
	Code      *code_core.Store
	Queue     *review_queue.Queue
	Overlay   *semantic_overlay.Overlay
	Registry  *kernel.Registry

	// Orch is the long-lived three-source orchestrator. Built once at
	// Open and reused for every IndexFile call driven by the watcher;
	// avoids the per-CLI-invocation rebuild cost in the daemon path.
	Orch *extract.Orchestrator

	// Watch is the fsnotify-driven watcher → orchestrator bridge per
	// SPEC §6.20. nil for short-lived non-daemon callers that opt out
	// of the watch loop (batch CLI, tests).
	Watch *WatchLoop

	codeDB  *sql.DB
	queueDB *sql.DB
}

// OpenOptions controls Resources.Open behaviour at the seams that
// differ between the long-lived daemon and a short-lived CLI. The
// zero value matches the historical behavior (no watcher, cold
// re-extract of every Go file).
type OpenOptions struct {
	// EnableWatcher starts the long-lived fsnotify → orchestrator
	// loop. Required for daemon mode; off by default so short-lived
	// CLI/test callers don't pay the goroutine cost.
	EnableWatcher bool

	// ExtractOptions tunes the orchestrator's enabled source set
	// (LSP / SCIP / tree-sitter). Defaults to the daemon-canonical
	// configuration when EnableWatcher is true.
	ExtractOptions extract.Options

	// ErrLog is an optional sink for non-fatal events from the
	// watcher / orchestrator goroutines. Nil discards.
	ErrLog func(format string, args ...any)
}

// Open initializes every kernel handle with the historical (no-watcher)
// defaults. The caller owns the returned Resources and must call Close.
// For the daemon path that needs the always-on watcher loop, use OpenWithOptions.
func Open(ctx context.Context, ws *Workspace) (*Resources, error) {
	return OpenWithOptions(ctx, ws, OpenOptions{})
}

// OpenWithOptions is Open with explicit opt-in for the watcher loop
// and the orchestrator's source set. Daemon mode passes
// OpenOptions{EnableWatcher: true} so the fsnotify → orchestrator
// bridge runs continuously; batch / test callers leave it false.
func OpenWithOptions(ctx context.Context, ws *Workspace, opts OpenOptions) (*Resources, error) {
	if !ws.IsInitialized() {
		return nil, fmt.Errorf("workspace not initialized at %s; run `graph-harness init` first", ws.Root)
	}

	log, err := facts.OpenEventLog(ws.EventLog)
	if err != nil {
		return nil, fmt.Errorf("event log: %w", err)
	}

	codeDSN := ws.EventLog + ".code.core?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	codeDB, err := sql.Open("sqlite", codeDSN)
	if err != nil {
		_ = log.Close()
		return nil, fmt.Errorf("code.core open: %w", err)
	}
	if err := codeDB.PingContext(ctx); err != nil {
		_ = log.Close()
		_ = codeDB.Close()
		return nil, fmt.Errorf("code.core ping: %w", err)
	}
	codeStore, err := code_core.NewStore(codeDB)
	if err != nil {
		_ = log.Close()
		_ = codeDB.Close()
		return nil, fmt.Errorf("code.core schema: %w", err)
	}
	// P0.5.T18 / SPEC §9.1: install the kernel trust policy so
	// future tokenized writes (Store.PutEntityWithToken) verify
	// authorities at the storage seam. Strict mode stays off during
	// the rollout; flipping it to true once every writer is migrated
	// is the final v1 ship-bar step for the writer-monopoly clause.
	codeStore.SetTrustPolicy(kernel.NewTrustPolicy())

	queueDSN := ws.EventLog + ".review.queue?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	queueDB, err := sql.Open("sqlite", queueDSN)
	if err != nil {
		_ = log.Close()
		_ = codeDB.Close()
		return nil, fmt.Errorf("review.queue open: %w", err)
	}
	if err := queueDB.PingContext(ctx); err != nil {
		_ = log.Close()
		_ = codeDB.Close()
		_ = queueDB.Close()
		return nil, fmt.Errorf("review.queue ping: %w", err)
	}
	queue, err := review_queue.NewQueue(queueDB)
	if err != nil {
		_ = log.Close()
		_ = codeDB.Close()
		_ = queueDB.Close()
		return nil, fmt.Errorf("review.queue schema: %w", err)
	}

	overlay := semantic_overlay.NewOverlay()
	if _, err := overlay.Load(ws.OverlayDir); err != nil {
		_ = log.Close()
		_ = codeDB.Close()
		_ = queueDB.Close()
		return nil, fmt.Errorf("overlay load: %w", err)
	}

	reg := kernel.NewRegistry()
	if err := reg.LoadEmbeddedManifests(); err != nil {
		// Non-fatal: dev tree without baked manifests.
		reg = kernel.NewRegistry()
	}

	r := &Resources{
		Workspace: ws,
		Log:       log,
		Code:      codeStore,
		Queue:     queue,
		Overlay:   overlay,
		Registry:  reg,
		codeDB:    codeDB,
		queueDB:   queueDB,
	}

	// Daemon mode: build the long-lived orchestrator + watcher loop.
	// Cold sweep MUST run through the same orchestrator the watcher
	// uses so the per-source confidence / provenance shape is
	// identical between cold-start and live-observation — without
	// that consistency every post-cold-sweep re-extract would falsely
	// look like a state transition (the fingerprint would shift from
	// IngestParsedFile's 1.0 confidence to ParsedFileToSymbols's
	// 0.95). SPEC §6.20 hydration parity assumes this single-path
	// invariant.
	if opts.EnableWatcher {
		unifier := &code_core.Unifier{
			Store:   codeStore,
			Emitter: codeCoreEmitter(log),
		}
		orch, err := extract.NewOrchestrator(ws.Root, codeStore, unifier, opts.ExtractOptions)
		if err != nil {
			_ = r.Close()
			return nil, fmt.Errorf("orchestrator: %w", err)
		}
		r.Orch = orch
		coldSeq := uint64(0)
		if log != nil {
			coldSeq = log.LastSeq()
		}
		if err := orch.IndexAll(ctx, coldSeq); err != nil {
			_ = r.Close()
			return nil, fmt.Errorf("orchestrator cold sweep: %w", err)
		}
		// SPEC §6.20: cold-start drift scan detects files that
		// disappeared since the last shutdown. Re-extract handled
		// the on-disk side; this pass handles the disappeared side
		// — every stored File whose path no longer exists emits
		// FileRemoved on the kernel bus and drops from code.core.
		if err := r.sweepDeletions(ctx); err != nil {
			_ = r.Close()
			return nil, fmt.Errorf("sweep deletions: %w", err)
		}
		watch, err := NewWatchLoop(ws.Root, orch, log, opts.ErrLog)
		if err != nil {
			_ = r.Close()
			return nil, fmt.Errorf("watch loop: %w", err)
		}
		if err := watch.Start(ctx); err != nil {
			_ = r.Close()
			return nil, fmt.Errorf("watch start: %w", err)
		}
		r.Watch = watch
	} else {
		// Non-watcher (batch / short-lived CLI) path keeps the
		// legacy tree-sitter-only cold sweep. Cheap on subsequent
		// runs because identity is content-addressable and (post
		// P0.5.T09) the unifier short-circuits no-op observations.
		if err := r.indexWorkspaceCode(ctx); err != nil {
			_ = r.Close()
			return nil, fmt.Errorf("initial index: %w", err)
		}
	}

	return r, nil
}

// sweepDeletions walks every stored File entity and removes the row
// (plus emits code.core.FileRemoved on the kernel bus) for any path
// whose disk file no longer exists. Called once on cold-start so the
// daemon's materialized state matches the workspace's actual file
// set at startup (SPEC §6.20 deletion half of hydration parity).
//
// Idempotent: re-running on a workspace whose stored Files all still
// exist emits zero events.
func (r *Resources) sweepDeletions(ctx context.Context) error {
	if r.Code == nil {
		return nil
	}
	paths, err := r.Code.ListFilePaths(ctx)
	if err != nil {
		return fmt.Errorf("list file paths: %w", err)
	}
	for _, rel := range paths {
		abs := filepath.Join(r.Workspace.Root, rel)
		if _, err := os.Stat(abs); err == nil {
			continue // still on disk
		} else if !os.IsNotExist(err) {
			// Permission or I/O error — keep the entry; the next
			// cold sweep retries.
			continue
		}
		// File is gone. Drop the entity and emit the event.
		fileID := code_core.FileID(rel)
		if err := r.Code.DeleteEntity(ctx, fileID); err != nil {
			return fmt.Errorf("delete entity %s: %w", rel, err)
		}
		if r.Log == nil {
			continue
		}
		payload, _ := json.Marshal(FileRemovedPayload{Path: rel})
		if _, err := r.Log.Append(ctx, []kernel.Event{{
			Layer:      "code.core",
			Kind:       "FileRemoved",
			Payload:    json.RawMessage(payload),
			ProducedBy: kernel.SourceClass("layer:code.core"),
		}}); err != nil {
			return fmt.Errorf("emit FileRemoved %s: %w", rel, err)
		}
	}
	return nil
}

// codeCoreEmitter mirrors cli.codeCoreEventEmitter — a thin adapter
// from facts.EventLog to the code_core.EventEmitter contract so the
// unifier can publish SymbolDisambiguation events without taking the
// kernel/facts dependency. Kept in daemon/ so OpenWithOptions does
// not introduce a circular import on internal/cli.
func codeCoreEmitter(log *facts.EventLog) code_core.EventEmitter {
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

// Close releases every handle. Idempotent. The watcher + orchestrator
// are stopped first so any in-flight re-extract goroutine drains
// before the underlying SQLite handles close.
func (r *Resources) Close() error {
	var firstErr error
	if r.Watch != nil {
		r.Watch.Close()
		r.Watch = nil
	}
	if r.Orch != nil {
		// Bound the LSP / SCIP shutdown so a hung child server does not
		// deadlock daemon teardown.
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := r.Orch.Close(shutCtx); err != nil && firstErr == nil {
			firstErr = err
		}
		cancel()
		r.Orch = nil
	}
	if r.Log != nil {
		if err := r.Log.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if r.codeDB != nil {
		if err := r.codeDB.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if r.queueDB != nil {
		if err := r.queueDB.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (r *Resources) indexWorkspaceCode(ctx context.Context) error {
	root := r.Workspace.Root
	files, err := sourceFilesUnder(root)
	if err != nil {
		return err
	}
	for _, abs := range files {
		// #nosec G304 -- abs originates from sourceFilesUnder under workspace root
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, abs)
		pf, err := source_live.ParseFile(rel, data)
		if err != nil || pf == nil {
			continue
		}
		seq := r.Log.LastSeq()
		if _, err := r.Code.IngestParsedFile(ctx, pf, seq); err != nil {
			return err
		}
	}
	return nil
}

// sourceFilesUnder returns every supported source file under root. The
// per-language dispatch lives in source_live.LanguageOf — this routine
// picks anything LanguageOf recognises so adding a language is a single
// change in source_live.
func sourceFilesUnder(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "bin" || name == "dist" || name == "__pycache__" {
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if source_live.LanguageOf(path) != "" {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}
