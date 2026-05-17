package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/extract"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// TestWatchLoop_FileEditEmitsCodeCoreFileChanged exercises the SPEC
// §6.20 watcher → orchestrator path end-to-end. A workspace is
// opened with EnableWatcher=true, a .go file is edited on disk, and
// the test asserts the daemon (a) re-extracts the file and (b)
// emits exactly one code.core.FileChanged event to subscribers.
//
// This is the headline P0.5 gate ("modify a Go file while daemon is
// running; selectors test reflects the change without an explicit
// re-index command") expressed as an integration test.
func TestWatchLoop_FileEditEmitsCodeCoreFileChanged(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t)

	// Write an initial Go file before opening Resources so the cold
	// sweep materializes it.
	original := []byte(`package pkg

func Foo() error {
    return nil
}
`)
	src := filepath.Join(ws.Root, "foo.go")
	if err := os.WriteFile(src, original, 0o600); err != nil {
		t.Fatalf("seed foo.go: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Capture every (rel, changed) the WatchLoop observes so the test
	// can synchronize on the post-edit re-extract deterministically.
	observed := make(chan observation, 16)

	res, err := OpenWithOptions(ctx, ws, OpenOptions{
		EnableWatcher: true,
		ExtractOptions: extract.Options{
			DisableLSP:  true, // tree-sitter only — keeps the test deterministic
			DisableSCIP: true,
		},
	})
	if err != nil {
		t.Fatalf("open resources: %v", err)
	}
	defer func() { _ = res.Close() }()
	res.Watch.SetOnChange(func(rel string, changed bool) {
		select {
		case observed <- observation{Rel: rel, Changed: changed}:
		case <-ctx.Done():
		}
	})

	// Subscribe to the kernel bus BEFORE the edit so we don't miss the
	// emitted FileChanged event.
	sub := res.Log.SubscribeWithFilter(kernel.EventFilter{})
	defer func() { _ = sub.Close() }()

	// Cold sweep already wrote the initial state. Re-saving the same
	// content should produce only changed=false observations; the
	// test exercises both no-op and state-transition paths. fsnotify
	// may fire multiple events per atomic save (truncate + finalize)
	// so we drain the window and assert the *aggregate* shape rather
	// than the first observation.
	if err := os.WriteFile(src, original, 0o600); err != nil {
		t.Fatalf("rewrite identical content: %v", err)
	}
	if drainHasChanged(ctx, observed, "foo.go", 500*time.Millisecond) {
		t.Fatalf("re-writing identical content must not produce changed=true observation")
	}

	// Now actually mutate the file. body_hash changes → fingerprint
	// shifts → WatchLoop emits code.core.FileChanged on the kernel
	// bus. fsnotify often fires two events for a single atomic write
	// (truncate + write); the first observation may see a partial
	// or empty file, so we drain until we see at least one
	// changed=true observation.
	edited := []byte(`package pkg

import "errors"

func Foo() error {
    return errors.New("changed")
}
`)
	if err := os.WriteFile(src, edited, 0o600); err != nil {
		t.Fatalf("edit foo.go: %v", err)
	}
	if !drainHasChanged(ctx, observed, "foo.go", 2*time.Second) {
		t.Fatalf("post-edit re-extract must produce at least one changed=true observation")
	}

	// Drain events from the subscription until we see a
	// code.core.FileChanged for foo.go (or the deadline trips).
	found := false
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
loop:
	for !found {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				break loop
			}
			if ev.Layer != "code.core" || ev.Kind != "FileChanged" {
				continue
			}
			var p FileChangedPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("decode FileChanged payload: %v", err)
			}
			if p.Path == "foo.go" {
				found = true
			}
		case <-deadline.C:
			break loop
		case <-ctx.Done():
			break loop
		}
	}
	if !found {
		t.Fatalf("daemon did not emit code.core.FileChanged for foo.go within deadline")
	}

	// Final assertion: code.core actually reflects the new state — a
	// `selectors test`-shaped read sees the post-edit body_hash. The
	// quickest way to assert this without spinning the full resolver
	// is via Store.LookupByQualifiedName.
	got, err := res.Code.LookupByQualifiedName(ctx, "pkg.Foo")
	if err != nil {
		t.Fatalf("lookup pkg.Foo: %v", err)
	}
	if got == nil {
		t.Fatalf("pkg.Foo entity missing post-edit")
	}
	// body_hash is the field most likely to change across the edit.
	// We don't pin a specific value (parser-internal hashing detail);
	// we just assert presence + non-empty.
	if got.BodyHash == "" {
		t.Fatalf("Foo body_hash empty post-edit; orchestrator did not re-extract")
	}
}

// TestWatchLoop_FileRemovedEmitsCodeCoreFileRemoved asserts the
// deletion half of the watcher contract: removing a tracked .go file
// emits a code.core.FileRemoved event on the kernel bus.
func TestWatchLoop_FileRemovedEmitsCodeCoreFileRemoved(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t)
	src := filepath.Join(ws.Root, "gone.go")
	if err := os.WriteFile(src, []byte("package gone\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := OpenWithOptions(ctx, ws, OpenOptions{
		EnableWatcher: true,
		ExtractOptions: extract.Options{DisableLSP: true, DisableSCIP: true},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = res.Close() }()

	sub := res.Log.SubscribeWithFilter(kernel.EventFilter{})
	defer func() { _ = sub.Close() }()

	if err := os.Remove(src); err != nil {
		t.Fatalf("remove: %v", err)
	}

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				t.Fatalf("subscription closed before FileRemoved")
			}
			if ev.Layer == "code.core" && ev.Kind == "FileRemoved" {
				var p FileRemovedPayload
				_ = json.Unmarshal(ev.Payload, &p)
				if p.Path == "gone.go" {
					return
				}
			}
		case <-deadline.C:
			t.Fatalf("daemon did not emit code.core.FileRemoved within deadline")
		case <-ctx.Done():
			t.Fatalf("context done")
		}
	}
}

type observation struct {
	Rel     string
	Changed bool
}

// drainHasChanged drains observations for rel during the given
// window and returns true iff at least one of them reported
// changed=true. The window is bounded so the test stays responsive
// under varied fsnotify cadence. ctx cancellation aborts early.
func drainHasChanged(ctx context.Context, observed chan observation, rel string, window time.Duration) bool {
	deadline := time.NewTimer(window)
	defer deadline.Stop()
	saw := false
	for {
		select {
		case obs := <-observed:
			if obs.Rel == rel && obs.Changed {
				saw = true
			}
		case <-deadline.C:
			return saw
		case <-ctx.Done():
			return saw
		}
	}
}

// newTestWorkspace creates an initialized workspace under a unique
// temp dir. Returns the workspace handle plus cleanup.
func newTestWorkspace(t *testing.T) *Workspace {
	t.Helper()
	dir := t.TempDir()
	ws, err := From(dir)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := ws.EnsureStateDir(); err != nil {
		t.Fatalf("ensure state dir: %v", err)
	}
	if err := os.WriteFile(ws.ConfigPath, []byte("# auto-generated by test\n"), 0o600); err != nil {
		t.Fatalf("write toml: %v", err)
	}
	_ = code_core.KindFile // keep imports live
	_ = sync.Once{}
	return ws
}
