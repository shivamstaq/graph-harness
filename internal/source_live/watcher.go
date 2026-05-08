package source_live

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// FileEventKind tags whether a watcher event reflects a content change,
// a metadata-only event (rename, mode change), or a removal.
type FileEventKind string

// File-event kinds.
const (
	FileEventChanged FileEventKind = "FileChanged"
	FileEventRemoved FileEventKind = "FileRemoved"
	FileEventParsed  FileEventKind = "FileParsed"
)

// FileEvent is the unit a Watcher emits on its Events() channel. For
// FileEventParsed, Parsed carries the structural extraction; for
// FileEventChanged and FileEventRemoved, Parsed is nil and the
// downstream consumer is expected to schedule a parse.
type FileEvent struct {
	Kind   FileEventKind
	Path   string // workspace-relative when constructed via the rooted helpers
	AbsDir string // directory the event originated from
	Parsed *ParsedFile
	Err    error // non-nil if a parse failed; Kind is still FileChanged in that case
}

// LanguageOf reports the canonical language id for a path based on its
// extension, or "" when the extension is not one of the supported
// languages. Centralising the mapping avoids skew between the watcher
// router and the LSP / SCIP language-detection paths.
func LanguageOf(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts", ".tsx", ".js", ".jsx":
		return "typescript"
	case ".py":
		return "python"
	default:
		return ""
	}
}

// ParseFile dispatches to the right per-language parser based on file
// extension and returns a ParsedFile. Returns (nil, nil) for files
// that aren't in a supported language — callers can ignore those
// cleanly without distinguishing "unsupported" from "empty".
func ParseFile(path string, src []byte) (*ParsedFile, error) {
	switch LanguageOf(path) {
	case "go":
		return ParseGoFile(path, src)
	case "typescript":
		return ParseTypeScriptFile(path, src)
	case "python":
		return ParsePythonFile(path, src)
	default:
		return nil, nil
	}
}

// Watcher is a multi-language fsnotify-backed file watcher that emits
// FileEvent values whenever a tracked source file changes on disk.
// Per SPEC §6.16 the watcher is the source of FileChanged + FileParsed
// events into the source.live layer.
//
// Construction is non-blocking; the goroutine that translates fsnotify
// events into FileEvents is spawned on Start. Stop drains and closes
// the events channel deterministically.
type Watcher struct {
	root    string
	w       *fsnotify.Watcher
	events  chan FileEvent
	dirs    map[string]struct{}
	dirsMu  sync.Mutex
	stopped chan struct{}
	once    sync.Once
}

// NewWatcher creates a Watcher rooted at root. The root is walked on
// Start to register all directories with fsnotify; new directories
// created during the watch are picked up via FileEventChanged on the
// parent and an inline AddDir call.
func NewWatcher(root string) (*Watcher, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify: %w", err)
	}
	return &Watcher{
		root:    abs,
		w:       w,
		events:  make(chan FileEvent, 64),
		dirs:    make(map[string]struct{}),
		stopped: make(chan struct{}),
	}, nil
}

// Events returns the receive-only event channel. Closed when Stop is
// called or when the underlying fsnotify watcher errors out.
func (w *Watcher) Events() <-chan FileEvent {
	return w.events
}

// Start begins watching. It walks the root once, registering every
// directory it finds, then spawns a goroutine to translate fsnotify
// events. Returns the first walk error (if any); subsequent errors
// are surfaced as FileEvent{Kind: FileChanged, Err: ...}.
func (w *Watcher) Start(ctx context.Context) error {
	if err := w.addRecursive(w.root); err != nil {
		return err
	}
	go w.run(ctx)
	return nil
}

// Stop terminates the watcher goroutine and closes the events channel.
// Idempotent.
func (w *Watcher) Stop() {
	w.once.Do(func() {
		_ = w.w.Close()
		<-w.stopped
		close(w.events)
	})
}

func (w *Watcher) addRecursive(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Permission denied on a sub-tree shouldn't abort the
			// whole watch — record but continue.
			if errors.Is(err, os.ErrPermission) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if shouldSkipDir(d.Name()) {
			return filepath.SkipDir
		}
		return w.addDir(path)
	})
}

func (w *Watcher) addDir(path string) error {
	w.dirsMu.Lock()
	defer w.dirsMu.Unlock()
	if _, ok := w.dirs[path]; ok {
		return nil
	}
	if err := w.w.Add(path); err != nil {
		return fmt.Errorf("watch %s: %w", path, err)
	}
	w.dirs[path] = struct{}{}
	return nil
}

// run translates fsnotify events into FileEvent values until the
// fsnotify channel closes or ctx is done.
func (w *Watcher) run(ctx context.Context) {
	defer close(w.stopped)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			w.handleFsnotify(ev)
		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			w.emit(FileEvent{Kind: FileEventChanged, Err: err})
		}
	}
}

func (w *Watcher) handleFsnotify(ev fsnotify.Event) {
	if ev.Op&fsnotify.Create == fsnotify.Create {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			if !shouldSkipDir(filepath.Base(ev.Name)) {
				_ = w.addDir(ev.Name)
			}
			return
		}
	}
	if ev.Op&fsnotify.Remove == fsnotify.Remove {
		w.emit(FileEvent{
			Kind:   FileEventRemoved,
			Path:   w.relPath(ev.Name),
			AbsDir: filepath.Dir(ev.Name),
		})
		return
	}
	if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
		return
	}
	if LanguageOf(ev.Name) == "" {
		return
	}
	relPath := w.relPath(ev.Name)
	w.emit(FileEvent{
		Kind:   FileEventChanged,
		Path:   relPath,
		AbsDir: filepath.Dir(ev.Name),
	})
	src, err := os.ReadFile(ev.Name)
	if err != nil {
		w.emit(FileEvent{
			Kind:   FileEventChanged,
			Path:   relPath,
			AbsDir: filepath.Dir(ev.Name),
			Err:    fmt.Errorf("read %s: %w", relPath, err),
		})
		return
	}
	pf, perr := ParseFile(relPath, src)
	if perr != nil {
		// Degrade gracefully: emit a low-confidence FileChanged carrying
		// the parse error. Downstream layers may choose to fall back
		// to LSP/SCIP-derived facts.
		w.emit(FileEvent{
			Kind:   FileEventChanged,
			Path:   relPath,
			AbsDir: filepath.Dir(ev.Name),
			Err:    perr,
		})
		return
	}
	if pf == nil {
		return
	}
	w.emit(FileEvent{
		Kind:   FileEventParsed,
		Path:   relPath,
		AbsDir: filepath.Dir(ev.Name),
		Parsed: pf,
	})
}

func (w *Watcher) relPath(abs string) string {
	r, err := filepath.Rel(w.root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(r)
}

func (w *Watcher) emit(ev FileEvent) {
	select {
	case w.events <- ev:
	case <-w.stopped:
	}
}

// shouldSkipDir excludes directories that produce a high volume of
// uninteresting events (VCS metadata, dependency caches, build trees).
// Keeping this conservative here means we don't drown downstream
// consumers in node_modules churn during npm installs.
func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn",
		"node_modules", "vendor",
		".venv", "venv", "__pycache__",
		"dist", "build", "target",
		".scip-index", ".graph-harness":
		return true
	}
	return false
}
