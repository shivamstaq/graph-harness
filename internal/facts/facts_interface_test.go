package facts

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// TestEventLogFacts_SatisfiesContract drives every Facts method
// through the EventLogFacts adapter once each. This is the F6
// post-migration smoke test: the Facts interface (SPEC §6.4) was
// dead code before P1.5 closure; now the adapter inhabits it and
// every method has at least one runtime exercise.
func TestEventLogFacts_SatisfiesContract(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	log, err := OpenEventLog(filepath.Join(tmp, "fl.db"))
	if err != nil {
		t.Fatalf("OpenEventLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	// Use the adapter through the typed interface to lock in the
	// shape: any future signature drift breaks compilation.
	var facts Facts = NewEventLogFacts(log, "")
	ctx := context.Background()

	// Write — two events, get back the commit seq.
	seq, err := facts.Write(ctx, []kernel.Event{
		{Layer: "code.core", Kind: "FileChanged", Payload: json.RawMessage(`{"path":"a.go"}`), ProducedBy: kernel.SourceLayerInternal},
		{Layer: "code.core", Kind: "FileChanged", Payload: json.RawMessage(`{"path":"b.go"}`), ProducedBy: kernel.SourceLayerInternal},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if seq != 2 {
		t.Fatalf("commit seq: want 2, got %d", seq)
	}

	// ReadCurrent — envelope at the current head; should carry both events.
	cur, err := facts.ReadCurrent(ctx, kernel.LayerQuery{})
	if err != nil {
		t.Fatalf("ReadCurrent: %v", err)
	}
	if cur.ResolvedAtSeq != 2 {
		t.Errorf("ReadCurrent.ResolvedAtSeq: want 2, got %d", cur.ResolvedAtSeq)
	}
	var cur1 []kernel.Event
	if err := json.Unmarshal(cur.Data, &cur1); err != nil {
		t.Fatalf("unmarshal current: %v", err)
	}
	if len(cur1) != 2 {
		t.Errorf("ReadCurrent events: want 2, got %d", len(cur1))
	}

	// ReadAsOf — envelope at seq=1; only first event visible.
	asof, err := facts.ReadAsOf(ctx, 1, kernel.LayerQuery{})
	if err != nil {
		t.Fatalf("ReadAsOf: %v", err)
	}
	if asof.ResolvedAtSeq != 1 {
		t.Errorf("ReadAsOf.ResolvedAtSeq: want 1, got %d", asof.ResolvedAtSeq)
	}
	var asof1 []kernel.Event
	_ = json.Unmarshal(asof.Data, &asof1)
	if len(asof1) != 1 {
		t.Errorf("ReadAsOf events at seq=1: want 1, got %d", len(asof1))
	}

	// Snapshot — produce a handle at the current head.
	snap, err := facts.Snapshot(ctx, 0) // 0 → current head
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Seq != 2 {
		t.Errorf("Snapshot.Seq: want 2, got %d", snap.Seq)
	}

	// Compact — delete events strictly before seq=2; snapshot proves recoverability.
	if err := facts.Compact(ctx, 2, snap); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	// After compaction, ReadAsOf(1) should return zero events (the row was deleted).
	asof, err = facts.ReadAsOf(ctx, 1, kernel.LayerQuery{})
	if err != nil {
		t.Fatalf("ReadAsOf post-compact: %v", err)
	}
	_ = json.Unmarshal(asof.Data, &asof1)
	if len(asof1) != 0 {
		t.Errorf("post-compact ReadAsOf(1): want 0 events, got %d", len(asof1))
	}

	// Restore — replay the snapshot's events; the live log grows by 1
	// (the surviving event at seq=2 is still there; restoring re-appends
	// the snapshot's contents, which include seq=2's event re-stamped
	// as a fresh seq).
	preSeq := log.LastSeq()
	if err := facts.Restore(ctx, snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	postSeq := log.LastSeq()
	if postSeq <= preSeq {
		t.Errorf("Restore should re-append events; pre=%d post=%d", preSeq, postSeq)
	}

	// Subscribe — register a fresh stream, write an event, observe it.
	stream, err := facts.Subscribe(ctx, kernel.EventFilter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if _, err := facts.Write(ctx, []kernel.Event{{
		Layer: "code.core", Kind: "FileChanged",
		Payload: json.RawMessage(`{"path":"c.go"}`), ProducedBy: kernel.SourceLayerInternal,
	}}); err != nil {
		t.Fatalf("Write post-subscribe: %v", err)
	}
	ev, ok := <-stream.Events()
	if !ok {
		t.Fatalf("stream closed before receiving event")
	}
	if ev.Kind != "FileChanged" {
		t.Errorf("stream event kind: want FileChanged, got %s", ev.Kind)
	}
}

// TestEventLogFacts_LayerScoped exercises the layer filter on
// ReadCurrent/ReadAsOf/Snapshot via the adapter.
func TestEventLogFacts_LayerScoped(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	log, err := OpenEventLog(filepath.Join(tmp, "scope.db"))
	if err != nil {
		t.Fatalf("OpenEventLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	scoped := NewEventLogFacts(log, "code.core")
	ctx := context.Background()
	_, _ = scoped.Write(ctx, []kernel.Event{
		{Layer: "code.core", Kind: "FileChanged", Payload: json.RawMessage(`{}`), ProducedBy: kernel.SourceLayerInternal},
		{Layer: "kernel.subscriptions", Kind: "SubscriberEvicted", Payload: json.RawMessage(`{}`), ProducedBy: kernel.SourceLayerInternal},
		{Layer: "code.core", Kind: "FileChanged", Payload: json.RawMessage(`{}`), ProducedBy: kernel.SourceLayerInternal},
	})
	env, err := scoped.ReadCurrent(ctx, kernel.LayerQuery{})
	if err != nil {
		t.Fatalf("ReadCurrent: %v", err)
	}
	var rows []kernel.Event
	_ = json.Unmarshal(env.Data, &rows)
	if len(rows) != 2 {
		t.Errorf("scoped ReadCurrent: want 2 code.core events, got %d", len(rows))
	}
	for _, ev := range rows {
		if ev.Layer != "code.core" {
			t.Errorf("scoped read returned %s", ev.Layer)
		}
	}
}
