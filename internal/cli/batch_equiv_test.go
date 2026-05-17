// Package cli — batch/daemon equivalence property test (P0.5.T17).
//
// The test asserts the SPEC §9.11 invariant: the same `validate-diff
// --diff <file>` invocation produces byte-identical output via the
// batch path (read-only SQLite, no daemon) and the daemon-routed path
// (JSON-RPC validate.diff) on the same workspace + same diff. The
// invariant is what gives consumers confidence that batch mode is a
// drop-in for CI while interactive use stays daemon-canonical.
//
// Approach (kept minimal-but-honest):
//
//   - Stand up a workspace fixture with a small Go file.
//   - Build the change_process.Pipeline once and run it twice against
//     the same diff at the same pinned seq — first directly (the
//     batch path), then through the JSON-RPC Service handler (the
//     daemon-routed path).
//   - Marshal both ValidateDiffResults to JSON via the canonical
//     encoder and assert byte equality.
//
// Using the JSON-RPC Service handler directly (rather than spinning
// up a real socket loop) is the same shape e2e tests use: the wire
// path is exercised by the existing subscriptions_test integration
// tests, and the invariant we are proving here is "same handler →
// same bytes," not "JSON-RPC framing is correct."
package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/shivamstaq/graph-harness/internal/change_process"
	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/review_queue"
	"github.com/shivamstaq/graph-harness/internal/semantic_overlay"
)

// TestValidateDiff_BatchAndDaemonProduceIdenticalOutput stands up a
// fixture workspace, runs the same diff through both paths, and
// asserts the byte-identical-output property (P0.5.T17 / SPEC §9.11).
func TestValidateDiff_BatchAndDaemonProduceIdenticalOutput(t *testing.T) {
	t.Parallel()
	ws, log, store, overlay, queue := newEquivFixture(t)

	// Pre-populate one code.core entity that the diff names; the
	// pipeline's stage-2 qualified-name lookup will find it and the
	// overlay's flow scope binds against it. Without the entity, the
	// pipeline emits zero findings — still a valid invariant, but
	// we want to exercise the non-trivial code path too.
	if err := store.PutEntity(context.Background(), code_core.Entity{
		ID:            "test-entity-1234567890abcdef",
		Kind:          code_core.KindFunction,
		LanguageID:    "go",
		QualifiedName: "checkout.Validate",
		Path:          "checkout/validator.go",
	}, 1); err != nil {
		t.Fatalf("seed entity: %v", err)
	}

	// Synthesize a minimal unified diff that names the function so
	// the pipeline runs end-to-end. Empty diffs short-circuit at
	// stage 1.
	diff := []byte(`--- a/checkout/validator.go
+++ b/checkout/validator.go
@@ -1,3 +1,3 @@
-func Validate() error { return nil }
+func Validate() error { return errors.New("blocked") }
`)

	// Path A: batch / in-process pipeline.
	pipelineA := &change_process.Pipeline{
		Overlay: overlay,
		Code:    store,
		Events:  log,
	}
	resA, err := pipelineA.ValidateDiff(context.Background(), diff, log.LastSeq())
	if err != nil {
		t.Fatalf("batch path: %v", err)
	}

	// Path B: through the JSON-RPC Service.ValidateDiff handler.
	reg := kernel.NewRegistry()
	svc := jsonrpc.NewService(ws, log, store, queue, reg, overlay)
	resB, err := svc.ValidateDiff(context.Background(), jsonrpc.ValidateDiffParams{Diff: string(diff)})
	if err != nil {
		t.Fatalf("daemon path: %v", err)
	}

	// Marshal both via the canonical encoder and compare byte-by-byte.
	bufA, errA := json.Marshal(resA)
	bufB, errB := json.Marshal(resB)
	if errA != nil || errB != nil {
		t.Fatalf("marshal: %v / %v", errA, errB)
	}
	if !bytes.Equal(bufA, bufB) {
		t.Fatalf("batch / daemon output diverged:\nbatch:  %s\ndaemon: %s", bufA, bufB)
	}
}

// TestValidateDiff_EmptyDiffIdenticalAcrossPaths confirms the
// short-circuit path produces identical output on both routes too.
// Regression catch for the case where one path returns nil findings
// and the other returns an empty slice.
func TestValidateDiff_EmptyDiffIdenticalAcrossPaths(t *testing.T) {
	t.Parallel()
	ws, log, store, overlay, queue := newEquivFixture(t)
	diff := []byte("--- a/x\n+++ b/x\n")

	pipelineA := &change_process.Pipeline{Overlay: overlay, Code: store, Events: log}
	resA, err := pipelineA.ValidateDiff(context.Background(), diff, log.LastSeq())
	if err != nil {
		t.Fatalf("batch: %v", err)
	}

	reg := kernel.NewRegistry()
	svc := jsonrpc.NewService(ws, log, store, queue, reg, overlay)
	resB, err := svc.ValidateDiff(context.Background(), jsonrpc.ValidateDiffParams{Diff: string(diff)})
	if err != nil {
		t.Fatalf("daemon: %v", err)
	}

	bufA, _ := json.Marshal(resA)
	bufB, _ := json.Marshal(resB)
	if !bytes.Equal(bufA, bufB) {
		t.Fatalf("empty-diff outputs diverged:\nbatch:  %s\ndaemon: %s", bufA, bufB)
	}
}

// newEquivFixture builds a minimal workspace + layer handles the
// equivalence tests share. The fixture is intentionally tiny — the
// goal is byte-equality across paths, not coverage of every layer.
func newEquivFixture(t *testing.T) (
	*daemon.Workspace,
	*facts.EventLog,
	*code_core.Store,
	*semantic_overlay.Overlay,
	*review_queue.Queue,
) {
	t.Helper()
	dir := t.TempDir()
	ws := &daemon.Workspace{
		Root:       dir,
		StateDir:   filepath.Join(dir, ".graph-harness"),
		EventLog:   filepath.Join(dir, "kernel.db"),
		SocketPath: filepath.Join(dir, "daemon.sock"),
		OverlayDir: filepath.Join(dir, ".graph-harness", "overlay"),
		ID:         "equiv-test",
	}
	_ = os.MkdirAll(ws.StateDir, 0o755)
	_ = os.MkdirAll(ws.OverlayDir, 0o755)

	log, err := facts.OpenEventLog(ws.EventLog)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	store := newEmptyCodeStoreEquiv(t, filepath.Join(dir, "code.db"))
	queue := newEmptyQueueEquiv(t, filepath.Join(dir, "queue.db"))
	overlay := semantic_overlay.NewOverlay()
	return ws, log, store, overlay, queue
}

func newEmptyCodeStoreEquiv(t *testing.T, path string) *code_core.Store {
	t.Helper()
	var db *sql.DB
	db, err := openEquivSQLite(path)
	if err != nil {
		t.Fatalf("open code.db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := code_core.NewStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func newEmptyQueueEquiv(t *testing.T, path string) *review_queue.Queue {
	t.Helper()
	db, err := openEquivSQLite(path)
	if err != nil {
		t.Fatalf("open queue.db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	q, err := review_queue.NewQueue(db)
	if err != nil {
		t.Fatalf("new queue: %v", err)
	}
	return q
}

func openEquivSQLite(path string) (*sql.DB, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	return sql.Open("sqlite", dsn)
}
