package jsonrpc

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/sourcegraph/jsonrpc2"
)

// TestTransientOverlay_PutGetDrop exercises the in-memory tier's
// core operations independently of the JSON-RPC surface. This is
// the unit-level guarantee P3/P4 consumers (IDE editor buffers,
// agent drafts, Studio form previews) rely on.
func TestTransientOverlay_PutGetDrop(t *testing.T) {
	t.Parallel()
	tr := NewTransientOverlay()

	tr.Put(TransientEntry{
		SubscriberID: "ext-vscode-1",
		SourceClass:  SourceClassEditor,
		TargetID:     "foo.go",
		Payload:      json.RawMessage(`{"text":"package pkg\n"}`),
	})

	got, ok := tr.Get("foo.go")
	if !ok {
		t.Fatalf("Get must return ok=true after Put")
	}
	if got.SourceClass != SourceClassEditor {
		t.Fatalf("wrong source_class: %s", got.SourceClass)
	}

	// Drop the entry; subsequent Get must miss.
	if !tr.Drop("ext-vscode-1", SourceClassEditor, "foo.go") {
		t.Fatalf("Drop should report true for existing entry")
	}
	if _, ok := tr.Get("foo.go"); ok {
		t.Fatalf("Get must miss after Drop")
	}
	// Idempotent Drop reports false.
	if tr.Drop("ext-vscode-1", SourceClassEditor, "foo.go") {
		t.Fatalf("Drop should report false for missing entry")
	}
}

// TestTransientOverlay_DropSubscriberEvictsEverything ensures the
// scope-to-session contract: removing a subscriber drops every
// entry that subscriber published.
func TestTransientOverlay_DropSubscriberEvictsEverything(t *testing.T) {
	t.Parallel()
	tr := NewTransientOverlay()
	for i, target := range []string{"a.go", "b.go", "c.go"} {
		tr.Put(TransientEntry{
			SubscriberID: "ext-vscode-1",
			SourceClass:  SourceClassEditor,
			TargetID:     target,
			Payload:      json.RawMessage(`{}`),
			SeqAtPublish: uint64(i),
		})
	}
	tr.Put(TransientEntry{
		SubscriberID: "ext-studio-1",
		SourceClass:  SourceClassStudioForm,
		TargetID:     "d.gh",
		Payload:      json.RawMessage(`{}`),
	})

	dropped := tr.DropSubscriber("ext-vscode-1")
	if dropped != 3 {
		t.Fatalf("DropSubscriber must remove all 3 vscode entries; got %d", dropped)
	}
	// Other subscriber's entry survives.
	if _, ok := tr.Get("d.gh"); !ok {
		t.Fatalf("DropSubscriber must not remove entries from other subscribers")
	}
}

// TestTransientOverlay_LRUEvictionFiresOverflow validates the
// per-subscriber LRU cap (SPEC §6.19). Exceeding the cap evicts
// the oldest entry from THAT subscriber and fires the onOverflow
// sink.
func TestTransientOverlay_LRUEvictionFiresOverflow(t *testing.T) {
	t.Parallel()
	tr := NewTransientOverlay()
	tr.SetCap(2)

	overflowC := make(chan TransientEntry, 4)
	tr.SetOverflowSink(func(e TransientEntry) {
		overflowC <- e
	})

	for i, target := range []string{"a.go", "b.go", "c.go"} {
		tr.Put(TransientEntry{
			SubscriberID: "sub",
			SourceClass:  SourceClassEditor,
			TargetID:     target,
			Payload:      json.RawMessage(`{}`),
			SeqAtPublish: uint64(i),
		})
	}

	select {
	case evicted := <-overflowC:
		if evicted.TargetID != "a.go" {
			t.Fatalf("overflow should evict oldest (a.go); got %s", evicted.TargetID)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("overflow sink not fired")
	}

	// The newest two entries survive.
	if _, ok := tr.Get("b.go"); !ok {
		t.Fatalf("b.go should still be present post-eviction")
	}
	if _, ok := tr.Get("c.go"); !ok {
		t.Fatalf("c.go should still be present post-eviction")
	}
}

// TestKernelTransient_DisconnectDropsTier exercises the JSON-RPC
// surface end-to-end: a client publishes a transient entry, the
// daemon's getTransient observes it, then closing the connection
// drops it (SPEC §6.19 scope-to-session).
func TestKernelTransient_DisconnectDropsTier(t *testing.T) {
	t.Parallel()
	srv, _, sockPath := startTestServer(t)
	defer srv.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	raw, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	stream := jsonrpc2.NewBufferedStream(raw, jsonrpc2.VSCodeObjectCodec{})
	conn := jsonrpc2.NewConn(ctx, stream, nil)

	// Identify so the daemon assigns a stable subscriber id.
	var idRes IdentifyResult
	if err := conn.Call(ctx, "kernel.identify", IdentifyParams{SubscriberID: "test-editor"}, &idRes); err != nil {
		t.Fatalf("identify: %v", err)
	}

	// Publish a transient editor buffer.
	var pubRes PublishTransientResult
	if err := conn.Call(ctx, "kernel.publishTransient", PublishTransientParams{
		SourceClass: SourceClassEditor,
		TargetID:    "foo.go",
		Payload:     json.RawMessage(`{"text":"package foo\nfunc Bar(){}"}`),
	}, &pubRes); err != nil {
		t.Fatalf("publishTransient: %v", err)
	}
	if pubRes.SubscriberID != "test-editor" {
		t.Fatalf("publishTransient must echo subscriber_id=test-editor; got %q", pubRes.SubscriberID)
	}

	// getTransient observes the published entry.
	var getRes GetTransientResult
	if err := conn.Call(ctx, "kernel.getTransient", GetTransientParams{TargetID: "foo.go"}, &getRes); err != nil {
		t.Fatalf("getTransient: %v", err)
	}
	if !getRes.Found {
		t.Fatalf("getTransient must report Found=true post-publish")
	}
	if getRes.Entry.SubscriberID != "test-editor" || getRes.Entry.SourceClass != SourceClassEditor {
		t.Fatalf("getTransient returned wrong entry: %+v", getRes.Entry)
	}

	// Close the connection. Disconnect drops the transient tier
	// entries tied to this subscriber.
	_ = conn.Close()
	// Give the server's disconnect path a moment to run DropSubscriber.
	time.Sleep(100 * time.Millisecond)

	// Reconnect and verify the entry is gone.
	raw2, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("redial: %v", err)
	}
	stream2 := jsonrpc2.NewBufferedStream(raw2, jsonrpc2.VSCodeObjectCodec{})
	conn2 := jsonrpc2.NewConn(ctx, stream2, nil)
	defer func() { _ = conn2.Close() }()

	var afterRes GetTransientResult
	if err := conn2.Call(ctx, "kernel.getTransient", GetTransientParams{TargetID: "foo.go"}, &afterRes); err != nil {
		t.Fatalf("getTransient post-disconnect: %v", err)
	}
	if afterRes.Found {
		t.Fatalf("transient entry should be dropped after originator disconnects; got %+v", afterRes.Entry)
	}
}

// TestKernelTransient_RejectsUnknownSourceClass enforces the SPEC
// §6.19 source-class closed set.
func TestKernelTransient_RejectsUnknownSourceClass(t *testing.T) {
	t.Parallel()
	srv, _, sockPath := startTestServer(t)
	defer srv.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	stream := jsonrpc2.NewBufferedStream(raw, jsonrpc2.VSCodeObjectCodec{})
	conn := jsonrpc2.NewConn(ctx, stream, nil)
	defer func() { _ = conn.Close() }()

	err = conn.Call(ctx, "kernel.publishTransient", PublishTransientParams{
		SourceClass: "not_a_real_class",
		TargetID:    "foo.go",
		Payload:     json.RawMessage(`{}`),
	}, nil)
	if err == nil {
		t.Fatalf("expected error for unknown source_class; got nil")
	}
}
