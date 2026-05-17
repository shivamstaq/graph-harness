package jsonrpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sourcegraph/jsonrpc2"
	_ "modernc.org/sqlite"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/review_queue"
	"github.com/shivamstaq/graph-harness/internal/semantic_overlay"
)

func openSQLite(path string) (*sql.DB, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	return sql.Open("sqlite", dsn)
}

// TestKernelSubscribe_PushesEventNotifications spins up the daemon's
// JSON-RPC server on a unix socket, opens a client that listens for
// `kernel.event` notifications, calls kernel.identify + subscribe,
// appends an event to the kernel log, and asserts the client
// receives a matching notification. This is the SPEC §6.22 round-
// trip the IDE / TUI / Studio / agent push surfaces all depend on.
func TestKernelSubscribe_PushesEventNotifications(t *testing.T) {
	t.Parallel()
	srv, log, sockPath := startTestServer(t)
	defer srv.Stop()

	// Client side: a notifications channel that collects every
	// kernel.event payload pushed by the daemon. The jsonrpc2
	// handler runs on the connection's read goroutine, so the
	// channel buffer must be generous.
	notes := make(chan EventNotification, 32)
	clientCtx, clientCancel := context.WithCancel(context.Background())
	defer clientCancel()

	raw, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	stream := jsonrpc2.NewBufferedStream(raw, jsonrpc2.VSCodeObjectCodec{})
	clientHandler := jsonrpc2.HandlerWithError(func(_ context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
		if req.Method != "kernel.event" {
			return nil, nil
		}
		var n EventNotification
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &n)
		}
		select {
		case notes <- n:
		default:
		}
		return nil, nil
	})
	clientConn := jsonrpc2.NewConn(clientCtx, stream, clientHandler)
	defer func() { _ = clientConn.Close() }()

	// identify + subscribe.
	var idRes IdentifyResult
	if err := clientConn.Call(clientCtx, "kernel.identify", IdentifyParams{SubscriberID: "test-client"}, &idRes); err != nil {
		t.Fatalf("identify: %v", err)
	}
	if idRes.SubscriberID != "test-client" {
		t.Fatalf("identify echo mismatch: got %q want %q", idRes.SubscriberID, "test-client")
	}
	var subRes SubscribeResult
	if err := clientConn.Call(clientCtx, "kernel.subscribe", SubscribeParams{Filter: ""}, &subRes); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if subRes.SubscriptionID == "" {
		t.Fatalf("subscribe returned empty subscription_id")
	}

	// Append a kernel event the subscriber should receive.
	if _, err := log.Append(clientCtx, []kernel.Event{{
		Layer:      "code.core",
		Kind:       "FileChanged",
		ProducedBy: kernel.SourceLayerInternal,
		Payload:    json.RawMessage(`{"path":"foo.go"}`),
	}}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Wait for the notification.
	select {
	case n := <-notes:
		if n.Layer != "code.core" || n.Kind != "FileChanged" {
			t.Fatalf("unexpected notification: %+v", n)
		}
		if n.SubscriptionID != subRes.SubscriptionID {
			t.Fatalf("notification subscription_id mismatch: got %q want %q", n.SubscriptionID, subRes.SubscriptionID)
		}
		if n.SubscriberID != "test-client" {
			t.Fatalf("notification subscriber_id mismatch: got %q want test-client", n.SubscriberID)
		}
		if n.Seq == 0 {
			t.Fatalf("notification has zero seq")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("did not receive kernel.event notification within deadline")
	}

	// Ack moves the cursor forward; subsequent reconnect would resume
	// from seq+1. We test the cursor advance via a follow-up event
	// with explicit cursor argument in a separate test (T03 below).
	if err := clientConn.Call(clientCtx, "kernel.ack", AckParams{
		SubscriptionID: subRes.SubscriptionID,
		Seq:            log.LastSeq(),
	}, nil); err != nil {
		t.Fatalf("ack: %v", err)
	}

	// Unsubscribe terminates the pump.
	if err := clientConn.Call(clientCtx, "kernel.unsubscribe", UnsubscribeParams{
		SubscriptionID: subRes.SubscriptionID,
	}, nil); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
}

// TestKernelSubscribe_FilterScopesNotifications verifies the
// layer/kind shorthand filter — only events matching the filter
// reach the subscriber.
func TestKernelSubscribe_FilterScopesNotifications(t *testing.T) {
	t.Parallel()
	srv, log, sockPath := startTestServer(t)
	defer srv.Stop()

	notes := make(chan EventNotification, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	raw, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	stream := jsonrpc2.NewBufferedStream(raw, jsonrpc2.VSCodeObjectCodec{})
	handler := jsonrpc2.HandlerWithError(func(_ context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
		if req.Method != "kernel.event" {
			return nil, nil
		}
		var n EventNotification
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &n)
		}
		select {
		case notes <- n:
		default:
		}
		return nil, nil
	})
	conn := jsonrpc2.NewConn(ctx, stream, handler)
	defer func() { _ = conn.Close() }()

	// Subscribe with a narrow filter: only code.core/FileChanged.
	var subRes SubscribeResult
	if err := conn.Call(ctx, "kernel.subscribe", SubscribeParams{Filter: "code.core/FileChanged"}, &subRes); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Append two events: one matches, one doesn't.
	if _, err := log.Append(ctx, []kernel.Event{
		{Layer: "code.core", Kind: "FileChanged", ProducedBy: kernel.SourceLayerInternal, Payload: json.RawMessage(`{}`)},
		{Layer: "code.core", Kind: "FileRemoved", ProducedBy: kernel.SourceLayerInternal, Payload: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Drain notifications for a window; only the FileChanged one
	// should be present.
	deadline := time.NewTimer(1 * time.Second)
	defer deadline.Stop()
	got := []EventNotification{}
loop:
	for {
		select {
		case n := <-notes:
			got = append(got, n)
		case <-deadline.C:
			break loop
		}
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 filtered notification, got %d: %+v", len(got), got)
	}
	if got[0].Kind != "FileChanged" {
		t.Fatalf("filter let through wrong event: %+v", got[0])
	}
}

// TestKernelSubscribe_MultiplexedSubscriptions verifies that a
// single connection can carry multiple subscriptions with
// independent filters and cursors (SPEC §6.22 multiplexing).
func TestKernelSubscribe_MultiplexedSubscriptions(t *testing.T) {
	t.Parallel()
	srv, log, sockPath := startTestServer(t)
	defer srv.Stop()

	type tagged struct {
		Note EventNotification
	}
	notes := make(chan tagged, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	raw, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	stream := jsonrpc2.NewBufferedStream(raw, jsonrpc2.VSCodeObjectCodec{})
	handler := jsonrpc2.HandlerWithError(func(_ context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
		if req.Method != "kernel.event" {
			return nil, nil
		}
		var n EventNotification
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &n)
		}
		select {
		case notes <- tagged{Note: n}:
		default:
		}
		return nil, nil
	})
	conn := jsonrpc2.NewConn(ctx, stream, handler)
	defer func() { _ = conn.Close() }()

	var subFC SubscribeResult
	if err := conn.Call(ctx, "kernel.subscribe", SubscribeParams{Filter: "code.core/FileChanged"}, &subFC); err != nil {
		t.Fatalf("subscribe FC: %v", err)
	}
	var subFR SubscribeResult
	if err := conn.Call(ctx, "kernel.subscribe", SubscribeParams{Filter: "code.core/FileRemoved"}, &subFR); err != nil {
		t.Fatalf("subscribe FR: %v", err)
	}
	if subFC.SubscriptionID == subFR.SubscriptionID {
		t.Fatalf("multiplexed subscriptions must have distinct IDs")
	}

	if _, err := log.Append(ctx, []kernel.Event{
		{Layer: "code.core", Kind: "FileChanged", ProducedBy: kernel.SourceLayerInternal, Payload: json.RawMessage(`{}`)},
		{Layer: "code.core", Kind: "FileRemoved", ProducedBy: kernel.SourceLayerInternal, Payload: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	deadline := time.NewTimer(1 * time.Second)
	defer deadline.Stop()
	counts := map[string]int{}
loop:
	for {
		select {
		case tg := <-notes:
			counts[tg.Note.SubscriptionID]++
		case <-deadline.C:
			break loop
		}
	}
	if counts[subFC.SubscriptionID] != 1 {
		t.Fatalf("FileChanged subscription expected 1 notification, got %d", counts[subFC.SubscriptionID])
	}
	if counts[subFR.SubscriptionID] != 1 {
		t.Fatalf("FileRemoved subscription expected 1 notification, got %d", counts[subFR.SubscriptionID])
	}
}

// startTestServer spins up a JSON-RPC server bound to a unix socket
// under a temp dir. Returns the server, the kernel event log (so
// tests can Append events), and the socket path.
func startTestServer(t *testing.T) (*Server, *facts.EventLog, string) {
	t.Helper()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")
	logPath := filepath.Join(dir, "kernel.db")

	log, err := facts.OpenEventLog(logPath)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	ws := &daemon.Workspace{
		Root:       dir,
		StateDir:   filepath.Join(dir, ".graph-harness"),
		EventLog:   logPath,
		SocketPath: sockPath,
		ID:         "test",
	}
	_ = os.MkdirAll(ws.StateDir, 0o755)

	// Minimal store + queue + overlay (none populated; tests don't
	// need them for the subscription path but the Service expects
	// non-nil values).
	store := newEmptyCodeStore(t, filepath.Join(dir, "code.db"))
	queue := newEmptyQueue(t, filepath.Join(dir, "queue.db"))
	overlay := semantic_overlay.NewOverlay()
	reg := kernel.NewRegistry()

	svc := NewService(ws, log, store, queue, reg, overlay)
	srv := NewServer(svc)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Serve(ctx, ln)
	}()
	t.Cleanup(func() { wg.Wait() })

	// Give the server a beat to register the listener.
	time.Sleep(10 * time.Millisecond)
	return srv, log, sockPath
}

func newEmptyCodeStore(t *testing.T, path string) *code_core.Store {
	t.Helper()
	db, err := openSQLite(path)
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

func newEmptyQueue(t *testing.T, path string) *review_queue.Queue {
	t.Helper()
	db, err := openSQLite(path)
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
