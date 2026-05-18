package facts

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// TestEventLog_CompactShrinksLog_PreservesRecoverability exercises
// F10 / P0.T06: Compact deletes events strictly below the cutoff
// when a snapshot at-or-above the cutoff exists, and the
// surviving + restored events round-trip to the pre-compact state.
func TestEventLog_CompactShrinksLog_PreservesRecoverability(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	log, err := OpenEventLog(filepath.Join(tmp, "fl.db"))
	if err != nil {
		t.Fatalf("OpenEventLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	ctx := context.Background()
	// Append 100 events.
	events := make([]kernel.Event, 100)
	for i := range events {
		events[i] = kernel.Event{
			Layer:      "code.core",
			Kind:       "FileChanged",
			Payload:    json.RawMessage(`{}`),
			ProducedBy: kernel.SourceLayerInternal,
		}
	}
	if _, err := log.Append(ctx, events); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got := log.LastSeq(); got != 100 {
		t.Fatalf("LastSeq: want 100, got %d", got)
	}

	// Retention = keep last 10 events; cutoff = 90.
	cutoff := uint64(90)
	snap, err := log.CreateSnapshot(ctx, "code.core", cutoff)
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if err := log.Compact(ctx, cutoff, snap); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// After compaction, ReadAsOf(89) should return zero events.
	asof, err := log.ReadAsOf(ctx, 89)
	if err != nil {
		t.Fatalf("ReadAsOf(89): %v", err)
	}
	if len(asof) != 0 {
		t.Errorf("post-compact ReadAsOf(89): want 0 events, got %d", len(asof))
	}

	// ReadAll should show only the 11 surviving events (seqs 90..100
	// inclusive — Compact deletes seq < cutoff AND seq <= snap.Seq;
	// snap.Seq=90, cutoff=90, so seq < 90 is deleted, seq >= 90 stays).
	all, err := log.ReadAll(ctx)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(all) != 11 {
		t.Errorf("post-compact ReadAll: want 11 surviving events (seq 90..100), got %d", len(all))
	}

	// Recoverability: LoadSnapshot replays the deleted prefix so the
	// pre-compact state can be reconstructed.
	replayed, err := log.LoadSnapshot(ctx, snap)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if len(replayed) != 90 {
		t.Errorf("LoadSnapshot replay count: want 90 events (seqs 1..90), got %d", len(replayed))
	}
}

// TestEventLog_CompactRefusesWithoutProof asserts the safety check:
// Compact refuses when the supplied snapshot doesn't cover the
// cutoff (so we don't destructively delete data we can't recover).
func TestEventLog_CompactRefusesWithoutProof(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	log, err := OpenEventLog(filepath.Join(tmp, "fl.db"))
	if err != nil {
		t.Fatalf("OpenEventLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	ctx := context.Background()
	for range 50 {
		_, _ = log.Append(ctx, []kernel.Event{{
			Layer: "code.core", Kind: "FileChanged",
			Payload: json.RawMessage(`{}`), ProducedBy: kernel.SourceLayerInternal,
		}})
	}
	// Snapshot at seq 10, attempt to compact at seq 40 — snap.Seq <
	// beforeSeq, refuse.
	snap, _ := log.CreateSnapshot(ctx, "code.core", 10)
	err = log.Compact(ctx, 40, snap)
	if err == nil {
		t.Fatal("Compact should refuse when snapshot covers a shorter prefix than the cutoff")
	}
}
