package code_core

import (
	"context"
	"sync"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/source_live/detect"
)

// recordingEmitter captures EmitCodeCoreEvent calls for assertions.
type recordingEmitter struct {
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	kind    string
	payload []byte
}

func (r *recordingEmitter) EmitCodeCoreEvent(_ context.Context, kind string, payload []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]byte, len(payload))
	copy(cp, payload)
	r.events = append(r.events, recordedEvent{kind: kind, payload: cp})
	return nil
}

// First emission for a (workspace, language, tool) tuple goes through;
// duplicates are suppressed.
func TestExtractorUnavailableEmitter_Idempotent(t *testing.T) {
	rec := &recordingEmitter{}
	em := NewExtractorUnavailableEmitter(rec)

	tool := detect.ToolReport{
		Name:   "pyright-langserver",
		Class:  detect.ToolClassLSP,
		Status: detect.StatusMissing,
		ProbeChain: []detect.ProbeStep{
			{Location: "$PATH", Found: false, Reason: "not on PATH"},
		},
		InstallHints: []detect.InstallHint{
			{Manager: "pipx", Command: "pipx install pyright", Preferred: true},
		},
	}

	for range 5 {
		if err := em.Emit(context.Background(), "/work/foo", "python", tool); err != nil {
			t.Fatalf("emit: %v", err)
		}
	}
	if len(rec.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(rec.events))
	}
	if rec.events[0].kind != EventKindExtractorUnavailable {
		t.Fatalf("kind: %q", rec.events[0].kind)
	}
}

// Different tools produce independent events.
func TestExtractorUnavailableEmitter_DistinctTools(t *testing.T) {
	rec := &recordingEmitter{}
	em := NewExtractorUnavailableEmitter(rec)

	for _, name := range []string{"pyright-langserver", "scip-python"} {
		tool := detect.ToolReport{Name: name, Class: detect.ToolClassLSP, Status: detect.StatusMissing}
		_ = em.Emit(context.Background(), "/work/foo", "python", tool)
	}
	if len(rec.events) != 2 {
		t.Fatalf("want 2 events, got %d", len(rec.events))
	}
}

// Reset clears the idempotency cache so the next emission re-fires.
func TestExtractorUnavailableEmitter_ResetReemits(t *testing.T) {
	rec := &recordingEmitter{}
	em := NewExtractorUnavailableEmitter(rec)
	tool := detect.ToolReport{Name: "scip-go", Class: detect.ToolClassSCIP, Status: detect.StatusMissing}
	_ = em.Emit(context.Background(), "/work/foo", "go", tool)
	em.Reset()
	_ = em.Emit(context.Background(), "/work/foo", "go", tool)
	if len(rec.events) != 2 {
		t.Fatalf("want 2 events after reset, got %d", len(rec.events))
	}
}

// Tools that are NOT missing don't emit events even when called.
func TestExtractorUnavailableEmitter_AvailableSkipped(t *testing.T) {
	rec := &recordingEmitter{}
	em := NewExtractorUnavailableEmitter(rec)
	tool := detect.ToolReport{Name: "gopls", Class: detect.ToolClassLSP, Status: detect.StatusAvailable}
	_ = em.Emit(context.Background(), "/work/foo", "go", tool)
	if len(rec.events) != 0 {
		t.Fatalf("want 0 events, got %d", len(rec.events))
	}
}

// Idempotency key matches detect.IdempotencyKey so the orchestrator and
// the emitter agree on dedup.
func TestExtractorUnavailableEmitter_KeyMatchesDetectHelper(t *testing.T) {
	want := detect.IdempotencyKey("/work/foo", "python", "pyright-langserver")
	tool := detect.ToolReport{Name: "pyright-langserver", Class: detect.ToolClassLSP, Status: detect.StatusMissing}
	_, got, err := MarshalExtractorUnavailable("/work/foo", "python", tool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got != want {
		t.Fatalf("key mismatch: %q vs %q", got, want)
	}
}
