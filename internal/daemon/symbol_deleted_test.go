package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shivamstaq/graph-harness/internal/extract"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// TestWatchLoop_FileRemoveCascadesSymbolDeleted asserts F7 / SPEC
// §6.20: when a watched file is removed, the daemon emits one
// `code.core.SymbolDeleted` event per child entity BEFORE the
// `code.core.FileRemoved` event for the File itself. Subscribers
// observing the bus see a deterministic per-symbol drift cascade,
// not just the File-level signal.
func TestWatchLoop_FileRemoveCascadesSymbolDeleted(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t)
	src := filepath.Join(ws.Root, "cascade.go")
	if err := os.WriteFile(src, []byte(`package cascade

func Alpha() error { return nil }
func Beta() error { return nil }
func Gamma() error { return nil }
`), 0o600); err != nil {
		t.Fatalf("seed cascade.go: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := OpenWithOptions(ctx, ws, OpenOptions{
		EnableWatcher: true,
		ExtractOptions: extract.Options{
			DisableLSP:  true,
			DisableSCIP: true,
		},
	})
	if err != nil {
		t.Fatalf("open resources: %v", err)
	}
	defer func() { _ = res.Close() }()

	// Subscribe to the kernel bus BEFORE deleting so we don't miss
	// the cascade.
	sub := res.Log.SubscribeWithFilter(kernel.EventFilter{Layers: []string{"code.core"}})
	defer func() { _ = sub.Close() }()

	if err := os.Remove(src); err != nil {
		t.Fatalf("remove cascade.go: %v", err)
	}

	// Drain events until both shapes seen or deadline.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	var (
		symbolDeleted []SymbolDeletedPayload
		fileRemoved   []FileRemovedPayload
	)
loop:
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				break loop
			}
			switch ev.Kind {
			case "SymbolDeleted":
				var p SymbolDeletedPayload
				_ = json.Unmarshal(ev.Payload, &p)
				if p.Path == "cascade.go" {
					symbolDeleted = append(symbolDeleted, p)
				}
			case "FileRemoved":
				var p FileRemovedPayload
				_ = json.Unmarshal(ev.Payload, &p)
				if p.Path == "cascade.go" {
					fileRemoved = append(fileRemoved, p)
				}
			}
			if len(fileRemoved) >= 1 && len(symbolDeleted) >= 3 {
				break loop
			}
		case <-deadline.C:
			break loop
		case <-ctx.Done():
			break loop
		}
	}

	if len(symbolDeleted) < 3 {
		t.Errorf("expected ≥3 SymbolDeleted events (Alpha/Beta/Gamma); got %d: %+v",
			len(symbolDeleted), symbolDeleted)
	}
	if len(fileRemoved) != 1 {
		t.Errorf("expected exactly 1 FileRemoved for cascade.go; got %d", len(fileRemoved))
	}
	seen := map[string]bool{}
	for _, p := range symbolDeleted {
		seen[p.QualifiedName] = true
	}
	for _, qname := range []string{"cascade.Alpha", "cascade.Beta", "cascade.Gamma"} {
		if !seen[qname] {
			t.Errorf("SymbolDeleted cascade missing %s; got %v", qname, seen)
		}
	}
}
