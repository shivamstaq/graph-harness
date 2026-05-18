package facts

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // SQLite driver registration

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

func newTestEventLog(t *testing.T) *EventLog {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.db")
	log, err := OpenEventLog(path)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func TestEventLog_AppendAssignsMonotonicSeqs(t *testing.T) {
	log := newTestEventLog(t)
	ctx := context.Background()
	last, err := log.Append(ctx, []kernel.Event{
		{Layer: "source.live", Kind: "FileChanged", ProducedBy: kernel.SourceExtractorTreeSitter, Payload: []byte(`{}`)},
		{Layer: "source.live", Kind: "FileParsed", ProducedBy: kernel.SourceExtractorTreeSitter, Payload: []byte(`{}`)},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if last != 2 {
		t.Errorf("last seq = %d, want 2", last)
	}
	got, err := log.ReadAll(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("readall len = %d, want 2", len(got))
	}
	if got[0].Seq != 1 || got[1].Seq != 2 {
		t.Errorf("seq order = [%d, %d], want [1, 2]", got[0].Seq, got[1].Seq)
	}
}

func TestEventLog_PersistsAndRereadsAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	log, err := OpenEventLog(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	_, err = log.Append(ctx, []kernel.Event{
		{Layer: "code.core", Kind: "EntityMaterialized", ProducedBy: kernel.SourceLayerInternal, Payload: []byte(`{"id":"abc"}`)},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	log2, err := OpenEventLog(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = log2.Close() })
	if got := log2.LastSeq(); got != 1 {
		t.Errorf("LastSeq after reopen = %d, want 1", got)
	}
	events, err := log2.ReadAll(ctx)
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if len(events) != 1 || events[0].Kind != "EntityMaterialized" {
		t.Errorf("events after reopen = %+v", events)
	}
}

func TestEventLog_SubscribeReceivesAppendedEvents(t *testing.T) {
	log := newTestEventLog(t)
	ctx := context.Background()
	sub := log.Subscribe(kernel.EventFilter{})
	defer func() { _ = sub.Close() }()

	go func() {
		_, _ = log.Append(ctx, []kernel.Event{
			{Layer: "x", Kind: "Hello", ProducedBy: kernel.SourceLayerInternal, Payload: []byte(`{}`)},
		})
	}()

	select {
	case ev := <-sub.Events():
		if ev.Kind != "Hello" {
			t.Errorf("got kind %q, want Hello", ev.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fanout")
	}
}

func TestEventLog_SubscribeWithFilterDropsNonMatches(t *testing.T) {
	log := newTestEventLog(t)
	ctx := context.Background()
	sub := log.SubscribeWithFilter(kernel.EventFilter{Layers: []string{"want"}})
	defer func() { _ = sub.Close() }()

	_, _ = log.Append(ctx, []kernel.Event{
		{Layer: "skip", Kind: "Skip", ProducedBy: kernel.SourceLayerInternal, Payload: []byte(`{}`)},
		{Layer: "want", Kind: "Want", ProducedBy: kernel.SourceLayerInternal, Payload: []byte(`{}`)},
	})

	select {
	case ev := <-sub.Events():
		if ev.Layer != "want" {
			t.Errorf("filter passed wrong event: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
}

func TestEventLog_CursorRoundTrip(t *testing.T) {
	log := newTestEventLog(t)
	ctx := context.Background()
	if err := log.AdvanceCursor(ctx, "code.core", "", 42); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if err := log.AdvanceCursor(ctx, "code.core", "", 100); err != nil {
		t.Fatalf("advance: %v", err)
	}
	got, err := log.CursorOf(ctx, "code.core", "")
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if got != 100 {
		t.Errorf("cursor = %d, want 100", got)
	}
}

// TestEventLog_PerSubscriptionCursors exercises F8 / P0.5.T02: two
// subscriptions under the same subscriber_id with different
// subscription_name values persist independent cursors. Without the
// compound PK the second AdvanceCursor would overwrite the first.
func TestEventLog_PerSubscriptionCursors(t *testing.T) {
	log := newTestEventLog(t)
	ctx := context.Background()
	if err := log.AdvanceCursor(ctx, "ide-vscode", "code.core", 17); err != nil {
		t.Fatalf("advance code.core: %v", err)
	}
	if err := log.AdvanceCursor(ctx, "ide-vscode", "semantic.overlay", 42); err != nil {
		t.Fatalf("advance semantic.overlay: %v", err)
	}
	if got, _ := log.CursorOf(ctx, "ide-vscode", "code.core"); got != 17 {
		t.Errorf("code.core cursor: want 17, got %d", got)
	}
	if got, _ := log.CursorOf(ctx, "ide-vscode", "semantic.overlay"); got != 42 {
		t.Errorf("semantic.overlay cursor: want 42, got %d", got)
	}
	// The unscoped query (legacy "") must NOT collide with either named
	// row; absent rows return zero.
	if got, _ := log.CursorOf(ctx, "ide-vscode", ""); got != 0 {
		t.Errorf("unscoped cursor: want 0 (no row), got %d", got)
	}
}

func TestEventLog_ReadAsOfBoundsByMaxSeq(t *testing.T) {
	log := newTestEventLog(t)
	ctx := context.Background()
	for range 5 {
		_, _ = log.Append(ctx, []kernel.Event{
			{Layer: "x", Kind: "Tick", ProducedBy: kernel.SourceLayerInternal, Payload: []byte(`{}`)},
		})
	}
	got, err := log.ReadAsOf(ctx, 3)
	if err != nil {
		t.Fatalf("readasof: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("readasof(3) returned %d events, want 3", len(got))
	}
}
