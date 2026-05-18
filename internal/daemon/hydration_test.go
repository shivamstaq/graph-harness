package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/extract"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// TestHydration_ColdRestartEqualsLiveObservation asserts SPEC §6.20
// hydration parity: the materialized state after a cold-restart
// where the disk has not changed since shutdown is byte-identical
// (entity-set-identical, fingerprint-identical) to the state from
// before shutdown. The cold-start drift scan must skip every file
// whose stored content_hash matches the current disk content.
//
// The test:
//  1. Opens a workspace with N Go files; cold sweep extracts them.
//  2. Captures the entity set + per-entity fingerprint.
//  3. Closes Resources (simulating daemon shutdown).
//  4. Re-opens Resources without touching the disk (cold-restart
//     with everything unchanged).
//  5. Asserts the entity set + fingerprints match exactly.
//  6. Asserts the second cold-sweep emitted zero state-transition
//     kernel events (no FileChanged, no SymbolDisambiguation).
func TestHydration_ColdRestartEqualsLiveObservation(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t)

	for i, src := range []string{
		`package alpha
func Alpha() error { return nil }
`,
		`package beta
func Beta(x int) int { return x + 1 }
`,
		`package gamma
type Gamma struct{}
func (g *Gamma) Run() error { return nil }
`,
	} {
		path := filepath.Join(ws.Root, []string{"alpha.go", "beta.go", "gamma.go"}[i])
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// First cold sweep — materialize the state.
	res1, err := OpenWithOptions(ctx, ws, OpenOptions{
		EnableWatcher: true,
		ExtractOptions: extract.Options{
			DisableLSP:  true,
			DisableSCIP: true,
		},
	})
	if err != nil {
		t.Fatalf("open #1: %v", err)
	}
	preState, err := snapshotEntities(ctx, res1.Code)
	if err != nil {
		t.Fatalf("snapshot pre-restart: %v", err)
	}
	preSeq := res1.Log.LastSeq()
	if err := res1.Close(); err != nil {
		t.Fatalf("close #1: %v", err)
	}

	// Second cold sweep — disk unchanged. Hydration parity invariant:
	// every per-file fast-path hits, zero new events emit.
	res2, err := OpenWithOptions(ctx, ws, OpenOptions{
		EnableWatcher: true,
		ExtractOptions: extract.Options{
			DisableLSP:  true,
			DisableSCIP: true,
		},
	})
	if err != nil {
		t.Fatalf("open #2: %v", err)
	}
	defer func() { _ = res2.Close() }()

	// Wait for any in-flight watcher activity from the initial walk
	// to settle (the watcher start triggers a directory-add storm on
	// some platforms). Give it a generous window so we don't false-
	// flag a fingerprint shift triggered by a stale fsnotify event.
	time.Sleep(300 * time.Millisecond)

	postState, err := snapshotEntities(ctx, res2.Code)
	if err != nil {
		t.Fatalf("snapshot post-restart: %v", err)
	}
	if !entitiesEqual(preState, postState) {
		t.Fatalf("cold-restart materialized state differs from pre-restart\npre=%v\npost=%v", preState, postState)
	}

	// Zero-events assertion: between preSeq and postSeq, no new
	// kernel events should be appended. The cold-start drift scan
	// fast-pathed every file; suppress-at-source guarded every
	// fingerprint comparison.
	postSeq := res2.Log.LastSeq()
	if postSeq != preSeq {
		// Surface what events DID fire so a future regression has
		// a paper trail.
		events := readEventsBetween(ctx, t, res2.Log, preSeq, postSeq)
		t.Fatalf("expected zero new kernel events after cold-restart; got %d (seq %d→%d): %v",
			len(events), preSeq, postSeq, events)
	}
}

// TestHydration_DeletedFileEmitsFileRemoved exercises the deletion
// half of the cold-start drift scan: a File entity in code.core
// whose disk path no longer exists is removed and emits
// code.core.FileRemoved on the kernel bus.
func TestHydration_DeletedFileEmitsFileRemoved(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t)
	path := filepath.Join(ws.Root, "doomed.go")
	if err := os.WriteFile(path, []byte("package doomed\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// First open: file lands in code.core.
	res1, err := OpenWithOptions(ctx, ws, OpenOptions{
		EnableWatcher: true,
		ExtractOptions: extract.Options{DisableLSP: true, DisableSCIP: true},
	})
	if err != nil {
		t.Fatalf("open #1: %v", err)
	}
	stored, err := res1.Code.LookupEntityByID(ctx, code_core.FileID("doomed.go"))
	if err != nil || stored == nil {
		t.Fatalf("doomed.go not in code.core post-cold-sweep: err=%v stored=%v", err, stored)
	}
	if err := res1.Close(); err != nil {
		t.Fatalf("close #1: %v", err)
	}

	// Delete the file while daemon is down.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// Second open: cold-start drift scan should detect the deletion
	// and emit FileRemoved.
	res2, err := OpenWithOptions(ctx, ws, OpenOptions{
		EnableWatcher: true,
		ExtractOptions: extract.Options{DisableLSP: true, DisableSCIP: true},
	})
	if err != nil {
		t.Fatalf("open #2: %v", err)
	}
	defer func() { _ = res2.Close() }()

	// The entity must be gone from code.core.
	stillThere, err := res2.Code.LookupEntityByID(ctx, code_core.FileID("doomed.go"))
	if err != nil {
		t.Fatalf("lookup post-delete: %v", err)
	}
	if stillThere != nil {
		t.Fatalf("doomed.go entity still present after cold-start deletion sweep")
	}

	// The kernel bus must have a code.core.FileRemoved event for it.
	events, err := res2.Log.ReadAsOf(ctx, res2.Log.LastSeq())
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	found := false
	for _, ev := range events {
		if ev.Layer != "code.core" || ev.Kind != "FileRemoved" {
			continue
		}
		var p FileRemovedPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		if p.Path == "doomed.go" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("cold-start drift scan did not emit code.core.FileRemoved for doomed.go")
	}
}

// entitySnapshot is one row's worth of identity used by the
// hydration parity test. Fingerprint summarizes content per
// Entity.ContentFingerprint so the test fails on any field shift.
type entitySnapshot struct {
	ID          string
	Kind        string
	Fingerprint string
}

func snapshotEntities(ctx context.Context, store *code_core.Store) ([]entitySnapshot, error) {
	ents, err := store.LookupAllEntities(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]entitySnapshot, 0, len(ents))
	for _, e := range ents {
		out = append(out, entitySnapshot{
			ID:          e.ID,
			Kind:        string(e.Kind),
			Fingerprint: e.ContentFingerprint(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func entitiesEqual(a, b []entitySnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func readEventsBetween(ctx context.Context, t *testing.T, log readAsOf, fromSeq, toSeq uint64) []string {
	t.Helper()
	events, err := log.ReadAsOf(ctx, toSeq)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var out []string
	for _, ev := range events {
		if ev.Seq <= fromSeq {
			continue
		}
		out = append(out, ev.Layer+"."+ev.Kind)
	}
	return out
}

// readAsOf abstracts the read seam so the helper can take either
// the real *facts.EventLog or a mock.
type readAsOf interface {
	ReadAsOf(ctx context.Context, seq uint64) ([]kernel.Event, error)
}
