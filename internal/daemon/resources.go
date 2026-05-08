package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // SQLite driver registered for the daemon path

	"github.com/shivamstaq/graph-harness/internal/code_core"
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

	codeDB  *sql.DB
	queueDB *sql.DB
}

// Open initializes every kernel handle. The caller owns the returned
// Resources and must call Close.
func Open(ctx context.Context, ws *Workspace) (*Resources, error) {
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

	// First-time index sweep: parse every Go file under the workspace
	// into code.core. Cheap on subsequent runs because identity is
	// content-addressable. The full multi-language sweep lands when
	// T-extractors' watcher is wired in (P1.C).
	if err := r.indexWorkspaceCode(ctx); err != nil {
		_ = r.Close()
		return nil, fmt.Errorf("initial index: %w", err)
	}

	return r, nil
}

// Close releases every handle. Idempotent.
func (r *Resources) Close() error {
	var firstErr error
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
