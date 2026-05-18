package facts

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// TestSnapshot_CreateLoadRoundtripsByteIdentical closes P0 gate 7:
// a snapshot created from the event log at seq N, then loaded back,
// replays the byte-identical event slice that was committed. SPEC
// §6.16 + §7 hydration paths assume this invariant — without it,
// cold-start hydration from snapshot diverges from live observation.
func TestSnapshot_CreateLoadRoundtripsByteIdentical(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	log, err := OpenEventLog(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatalf("OpenEventLog: %v", err)
	}
	defer func() { _ = log.Close() }()

	events := []kernel.Event{
		{Layer: "code.core", Kind: "FileChanged", Payload: []byte(`{"path":"a.go"}`), ProducedBy: kernel.SourceExtractorTreeSitter},
		{Layer: "code.core", Kind: "SymbolDisambiguation", Payload: []byte(`{"path":"a.go","claims":[]}`), ProducedBy: kernel.SourceLayerInternal},
		{Layer: "semantic.overlay", Kind: "OverlayItemImported", Payload: []byte(`{"name":"X"}`), ProducedBy: kernel.SourceUser},
	}
	if _, err := log.Append(context.Background(), events); err != nil {
		t.Fatalf("Append: %v", err)
	}

	handle, err := log.CreateSnapshot(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if handle.Seq != log.LastSeq() {
		t.Fatalf("snapshot seq: got %d want %d", handle.Seq, log.LastSeq())
	}

	loaded, err := log.LoadSnapshot(context.Background(), handle)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	live, err := log.ReadAll(context.Background())
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(loaded) != len(live) {
		t.Fatalf("len: snapshot=%d live=%d", len(loaded), len(live))
	}
	for i := range loaded {
		if loaded[i].Seq != live[i].Seq || loaded[i].Layer != live[i].Layer ||
			loaded[i].Kind != live[i].Kind || string(loaded[i].Payload) != string(live[i].Payload) ||
			loaded[i].ProducedBy != live[i].ProducedBy {
			t.Errorf("event[%d] differs: snap=%+v live=%+v", i, loaded[i], live[i])
		}
	}
}

// TestSnapshot_LayerFilter asserts the per-layer filter — a
// code.core-only snapshot doesn't replay semantic.overlay events.
func TestSnapshot_LayerFilter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	log, err := OpenEventLog(filepath.Join(dir, "kernel.db"))
	if err != nil {
		t.Fatalf("OpenEventLog: %v", err)
	}
	defer func() { _ = log.Close() }()
	if _, err := log.Append(context.Background(), []kernel.Event{
		{Layer: "code.core", Kind: "FileChanged", Payload: []byte(`{}`), ProducedBy: kernel.SourceExtractorTreeSitter},
		{Layer: "semantic.overlay", Kind: "OverlayItemImported", Payload: []byte(`{}`), ProducedBy: kernel.SourceUser},
		{Layer: "code.core", Kind: "FileChanged", Payload: []byte(`{}`), ProducedBy: kernel.SourceExtractorTreeSitter},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	handle, err := log.CreateSnapshot(context.Background(), "code.core", 0)
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	loaded, err := log.LoadSnapshot(context.Background(), handle)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("layer filter: got %d, want 2", len(loaded))
	}
	for i, ev := range loaded {
		if ev.Layer != "code.core" {
			t.Errorf("event[%d] layer=%s, want code.core", i, ev.Layer)
		}
	}
}
