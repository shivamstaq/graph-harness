package scip

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// Event is one delivery on Refresher.Events. It carries the imported
// symbols for a single index file plus the language id its contents
// were attributed to. Err is non-nil if the index failed to decode.
type Event struct {
	IndexPath  string
	LanguageID string
	Symbols    []source_live.Symbol
	Err        error
}

// Refresher watches the workspace's .scip-index/ directory for index
// changes and re-imports affected indexes. It also exposes RefreshAll
// for the `graph-harness scip refresh` CLI hook (a manual full
// re-import).
//
// Concurrency: Refresher runs a single fsnotify-fed goroutine; events
// fan out on Events. Stop is idempotent.
type Refresher struct {
	root      string
	indexDir  string
	importers map[string]Importer
	w         *fsnotify.Watcher
	events    chan Event
	stopped   chan struct{}
	once      sync.Once
}

// NewRefresher creates a Refresher rooted at root. importers maps
// language ids to their per-language Importer; pass nil to use the
// built-in three (go, typescript, python).
func NewRefresher(root string, importers map[string]Importer) (*Refresher, error) {
	if importers == nil {
		importers = DefaultImporters()
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("scip refresher root: %w", err)
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("scip refresher: %w", err)
	}
	indexDir := filepath.Join(abs, ".scip-index")
	return &Refresher{
		root:      abs,
		indexDir:  indexDir,
		importers: importers,
		w:         w,
		events:    make(chan Event, 16),
		stopped:   make(chan struct{}),
	}, nil
}

// DefaultImporters returns the built-in three SCIP importers (go, ts,
// python).
func DefaultImporters() map[string]Importer {
	return map[string]Importer{
		"go":         NewGoImporter(),
		"typescript": NewTSImporter(),
		"python":     NewPyImporter(),
	}
}

// Events returns the receive-only event channel. Closed when Stop is
// called or when the underlying fsnotify watcher errors out.
func (r *Refresher) Events() <-chan Event { return r.events }

// Start begins watching the index directory. Returns nil if the
// directory does not exist (a workspace without SCIP indexes degrades
// gracefully); the caller can RefreshAll later if a directory is
// created out-of-band.
func (r *Refresher) Start(ctx context.Context) error {
	if _, err := os.Stat(r.indexDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			go r.run(ctx)
			return nil
		}
		return fmt.Errorf("stat index dir: %w", err)
	}
	if err := r.w.Add(r.indexDir); err != nil {
		return fmt.Errorf("watch %s: %w", r.indexDir, err)
	}
	go r.run(ctx)
	return nil
}

// Stop closes the watcher and the events channel.
func (r *Refresher) Stop() {
	r.once.Do(func() {
		_ = r.w.Close()
		<-r.stopped
		close(r.events)
	})
}

// RefreshAll re-imports every .scip file currently under the index
// directory, emitting one Event per index. Useful for the
// `graph-harness scip refresh` CLI command.
func (r *Refresher) RefreshAll() {
	paths, err := FindIndexesIn(r.indexDir)
	if err != nil {
		r.emit(Event{Err: err})
		return
	}
	for _, p := range paths {
		r.refreshOne(p)
	}
}

// RefreshOne re-imports a single index file by path. Path may be
// absolute or workspace-relative; missing files are surfaced as
// Event{Err}.
func (r *Refresher) RefreshOne(path string) {
	r.refreshOne(path)
}

func (r *Refresher) refreshOne(path string) {
	idx, err := Read(path)
	if err != nil {
		r.emit(Event{IndexPath: path, Err: err})
		return
	}
	for langID, imp := range r.importers {
		syms := imp.Import(idx, "")
		if len(syms) == 0 {
			continue
		}
		r.emit(Event{IndexPath: path, LanguageID: langID, Symbols: syms})
	}
}

func (r *Refresher) run(ctx context.Context) {
	defer close(r.stopped)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-r.w.Events:
			if !ok {
				return
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			if filepath.Ext(ev.Name) != ".scip" {
				continue
			}
			r.refreshOne(ev.Name)
		case err, ok := <-r.w.Errors:
			if !ok {
				return
			}
			r.emit(Event{Err: err})
		}
	}
}

func (r *Refresher) emit(ev Event) {
	select {
	case r.events <- ev:
	case <-r.stopped:
	}
}
