package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/shivamstaq/graph-harness/internal/extract"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// WatchLoop owns the long-lived fsnotify watcher plus the goroutine
// that translates filesystem events into incremental re-extracts
// against a daemon-resident Orchestrator. It is the seam SPEC §6.20
// (hydration model) + §6.21 (suppress-at-source) presume: a single
// always-on input loop that observes disk changes, re-extracts only
// the affected file, and emits drift events on the kernel bus only
// when state actually transitions.
//
// One WatchLoop per Resources. Construction is non-blocking; Start
// spawns the consumer goroutine. Close stops the loop deterministically.
type WatchLoop struct {
	root    string
	watcher *source_live.Watcher
	orch    *extract.Orchestrator
	log     *facts.EventLog

	// cancel terminates the loop's context-scoped goroutine.
	cancel context.CancelFunc

	// done signals the consumer goroutine exited so Close can join.
	done chan struct{}

	// onChange is optional: tests inject a synchronization hook that
	// observes every file-change re-extract. Nil in production.
	onChange func(rel string, changed bool)

	// errLog is the daemon's structured-log sink for non-fatal events
	// (parse failures, partial re-extract errors). Nil discards.
	errLog func(format string, args ...any)

	stoppedOnce sync.Once
}

// FileChangedPayload is the kernel-bus payload emitted when a watcher-
// driven re-extract detects a code.core state transition. Subscribers
// (TUI Findings panel, Studio impact view, IDE code lens via the P0.5
// push channel) decode this to know which file's facts moved.
type FileChangedPayload struct {
	Path     string `json:"path"`
	Language string `json:"language,omitempty"`
}

// FileRemovedPayload mirrors FileChangedPayload for deletes.
type FileRemovedPayload struct {
	Path string `json:"path"`
}

// NewWatchLoop builds a WatchLoop bound to the given orchestrator,
// event log, and workspace root. The watcher is constructed but not
// started — call Start to begin observing fsnotify events.
func NewWatchLoop(root string, orch *extract.Orchestrator, log *facts.EventLog, errLog func(format string, args ...any)) (*WatchLoop, error) {
	w, err := source_live.NewWatcher(root)
	if err != nil {
		return nil, fmt.Errorf("watcher: %w", err)
	}
	return &WatchLoop{
		root:    root,
		watcher: w,
		orch:    orch,
		log:     log,
		errLog:  errLog,
	}, nil
}

// SetOnChange installs a synchronization hook used by tests to observe
// every per-file re-extract decision. Not for production use.
func (l *WatchLoop) SetOnChange(f func(rel string, changed bool)) {
	l.onChange = f
}

// Start begins observing. Returns the first walk error (if any);
// subsequent fsnotify errors are surfaced through the watcher's own
// event channel and logged via errLog.
func (l *WatchLoop) Start(ctx context.Context) error {
	ctx, l.cancel = context.WithCancel(ctx)
	l.done = make(chan struct{})
	if err := l.watcher.Start(ctx); err != nil {
		return err
	}
	go l.consume(ctx)
	return nil
}

// Close terminates the loop and joins the consumer goroutine.
// Idempotent.
func (l *WatchLoop) Close() {
	l.stoppedOnce.Do(func() {
		if l.cancel != nil {
			l.cancel()
		}
		l.watcher.Stop()
		if l.done != nil {
			<-l.done
		}
	})
}

func (l *WatchLoop) logf(format string, args ...any) {
	if l.errLog != nil {
		l.errLog(format, args...)
	}
}

// consume is the long-lived translator: every watcher event becomes
// an orchestrator.IndexFileChanged call (write events) or a deletion
// path (remove events). State transitions emit `code.core.FileChanged`
// or `code.core.FileRemoved` kernel events; idempotent re-observations
// produce nothing.
func (l *WatchLoop) consume(ctx context.Context) {
	defer close(l.done)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-l.watcher.Events():
			if !ok {
				return
			}
			l.handle(ctx, ev)
		}
	}
}

func (l *WatchLoop) handle(ctx context.Context, ev source_live.FileEvent) {
	if ev.Err != nil {
		l.logf("watch: %s: %v", ev.Path, ev.Err)
		return
	}
	switch ev.Kind {
	case source_live.FileEventRemoved:
		l.handleRemoved(ctx, ev.Path)
	case source_live.FileEventChanged, source_live.FileEventParsed:
		l.handleChanged(ctx, ev.Path)
	}
}

func (l *WatchLoop) handleChanged(ctx context.Context, rel string) {
	if rel == "" {
		return
	}
	seq := uint64(0)
	if l.log != nil {
		seq = l.log.LastSeq()
	}
	changed, err := l.orch.IndexFileChanged(ctx, rel, seq)
	if l.onChange != nil {
		l.onChange(rel, changed)
	}
	if err != nil {
		l.logf("watch: re-extract %s: %v", rel, err)
		return
	}
	if !changed {
		return
	}
	if l.log == nil {
		return
	}
	payload, _ := json.Marshal(FileChangedPayload{
		Path:     rel,
		Language: source_live.LanguageOf(rel),
	})
	if _, err := l.log.Append(ctx, []kernel.Event{{
		Layer:      "code.core",
		Kind:       "FileChanged",
		Payload:    json.RawMessage(payload),
		ProducedBy: kernel.SourceClass("layer:code.core"),
	}}); err != nil {
		l.logf("watch: emit FileChanged %s: %v", rel, err)
	}
}

func (l *WatchLoop) handleRemoved(ctx context.Context, rel string) {
	if rel == "" || l.log == nil {
		return
	}
	payload, _ := json.Marshal(FileRemovedPayload{Path: rel})
	if _, err := l.log.Append(ctx, []kernel.Event{{
		Layer:      "code.core",
		Kind:       "FileRemoved",
		Payload:    json.RawMessage(payload),
		ProducedBy: kernel.SourceClass("layer:code.core"),
	}}); err != nil {
		l.logf("watch: emit FileRemoved %s: %v", rel, err)
	}
}
