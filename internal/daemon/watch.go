package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/shivamstaq/graph-harness/internal/extract"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// DefaultCoalesceWindow is the SPEC §6.21 watermark-coalescing
// window: filesystem events for the same path within this window
// collapse to one re-extract. Editor saves typically fire two
// fsnotify Write events (truncate + finalize) within a few
// milliseconds; build tools (formatters, linters) often touch
// many files in a burst. 50 ms is small enough that hover-time
// freshness stays sub-100 ms and large enough to absorb the
// common-case burst patterns. Tunable per WatchLoop.
const DefaultCoalesceWindow = 50 * time.Millisecond

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

	// coalesceWindow is the SPEC §6.21 watermark-coalescing window.
	// Defaults to DefaultCoalesceWindow when zero. Tests can shorten
	// it via SetCoalesceWindow to keep round-trip latencies tight.
	coalesceWindow time.Duration

	stoppedOnce sync.Once
}

// SetCoalesceWindow overrides the default fsnotify coalescing
// window. Must be called before Start. A zero or negative window
// disables coalescing — every fsnotify event drives a re-extract
// immediately (testing-only; not recommended in production).
func (l *WatchLoop) SetCoalesceWindow(d time.Duration) {
	l.coalesceWindow = d
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
//
// SPEC §6.21 watermark coalescing: fsnotify events for the same path
// within coalesceWindow collapse to a single re-extract. Editor saves
// typically emit two events (truncate + finalize) within a few ms;
// build-tool sweeps (formatters, codegens) can fire thousands in a
// burst. Coalescing keeps the orchestrator from re-parsing the same
// file repeatedly within a single save burst.
//
// Removal events are NOT coalesced — a deletion is final; if a path
// is recreated, the next write event handles the re-extract.
func (l *WatchLoop) consume(ctx context.Context) {
	defer close(l.done)

	window := l.coalesceWindow
	if window <= 0 {
		window = 0 // explicitly disabled — fast-path below
	}

	// pending tracks per-path "needs re-extract" with the latest
	// observed kind (Changed vs Parsed; the orchestrator treats them
	// the same). A negative timer (timer == nil) means coalescing is
	// disabled and we process events synchronously.
	pending := map[string]source_live.FileEvent{}
	var timer *time.Timer
	var flushC <-chan time.Time

	flush := func() {
		for path, ev := range pending {
			l.handleChanged(ctx, path)
			_ = ev
		}
		pending = map[string]source_live.FileEvent{}
		timer = nil
		flushC = nil
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-l.watcher.Events():
			if !ok {
				// Drain pending before exiting so any in-flight save
				// finishes its re-extract.
				if len(pending) > 0 {
					flush()
				}
				return
			}
			if ev.Err != nil {
				l.logf("watch: %s: %v", ev.Path, ev.Err)
				continue
			}
			switch ev.Kind {
			case source_live.FileEventRemoved:
				// Removals bypass coalescing. If a path was pending
				// re-extract and now removed, drop the pending entry
				// (the file's gone — no point parsing the empty space).
				delete(pending, ev.Path)
				l.handleRemoved(ctx, ev.Path)
			case source_live.FileEventChanged, source_live.FileEventParsed:
				if window <= 0 {
					l.handleChanged(ctx, ev.Path)
					continue
				}
				pending[ev.Path] = ev
				if timer == nil {
					timer = time.NewTimer(window)
					flushC = timer.C
				} else {
					// Reset extends the window for the latest burst —
					// editor saves that span longer than window stay
					// collapsed under a single re-extract.
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(window)
					flushC = timer.C
				}
			}
		case <-flushC:
			flush()
		}
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
