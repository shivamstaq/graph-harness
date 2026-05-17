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

// TestKernelSubscribe_BackpressureEmitsFellBehind exercises SPEC
// §6.22 backpressure: when the un-acked gap (cursor - ackedSeq)
// exceeds the per-subscription queue cap, the daemon emits a one-
// shot `kernel.fellBehind` notification carrying the recovery
// options. Subsequent acks that move the gap back under cap re-arm
// the flag.
func TestKernelSubscribe_BackpressureEmitsFellBehind(t *testing.T) {
	t.Parallel()
	srv, log, sockPath := startTestServer(t)
	defer srv.Stop()

	events := make(chan EventNotification, 64)
	fellBehinds := make(chan FellBehindNotification, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	raw, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	stream := jsonrpc2.NewBufferedStream(raw, jsonrpc2.VSCodeObjectCodec{})
	handler := jsonrpc2.HandlerWithError(func(_ context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
		switch req.Method {
		case "kernel.event":
			var n EventNotification
			if req.Params != nil {
				_ = json.Unmarshal(*req.Params, &n)
			}
			select {
			case events <- n:
			default:
			}
		case "kernel.fellBehind":
			var n FellBehindNotification
			if req.Params != nil {
				_ = json.Unmarshal(*req.Params, &n)
			}
			select {
			case fellBehinds <- n:
			default:
			}
		}
		return nil, nil
	})
	conn := jsonrpc2.NewConn(ctx, stream, handler)
	defer func() { _ = conn.Close() }()

	// Subscribe with a tiny queue cap so backpressure trips quickly.
	var subRes SubscribeResult
	if err := conn.Call(ctx, "kernel.subscribe", SubscribeParams{
		Filter:   "",
		QueueCap: 2,
	}, &subRes); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Append 5 events WITHOUT calling kernel.ack. The un-acked gap
	// will grow past the cap of 2; the daemon must fire one
	// kernel.fellBehind notification.
	for i := range 5 {
		if _, err := log.Append(ctx, []kernel.Event{{
			Layer:      "code.core",
			Kind:       "FileChanged",
			ProducedBy: kernel.SourceLayerInternal,
			Payload:    json.RawMessage(`{"i":` + string(rune('0'+i)) + `}`),
		}}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	select {
	case fb := <-fellBehinds:
		if fb.SubscriptionID != subRes.SubscriptionID {
			t.Fatalf("fellBehind subscription_id mismatch: got %q want %q", fb.SubscriptionID, subRes.SubscriptionID)
		}
		if fb.QueueCapacity != 2 {
			t.Fatalf("fellBehind queue_capacity mismatch: got %d want 2", fb.QueueCapacity)
		}
		if len(fb.Options) != 3 {
			t.Fatalf("fellBehind options should list 3 recovery modes, got %v", fb.Options)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("expected kernel.fellBehind notification within deadline")
	}

	// Verify the FellBehind is one-shot: a second batch without ack
	// should NOT produce another notification.
	for i := range 5 {
		if _, err := log.Append(ctx, []kernel.Event{{
			Layer:      "code.core",
			Kind:       "FileChanged",
			ProducedBy: kernel.SourceLayerInternal,
			Payload:    json.RawMessage(`{"j":` + string(rune('0'+i)) + `}`),
		}}); err != nil {
			t.Fatalf("append #2 %d: %v", i, err)
		}
	}
	select {
	case fb := <-fellBehinds:
		t.Fatalf("duplicate fellBehind should be suppressed; got %+v", fb)
	case <-time.After(300 * time.Millisecond):
		// Expected — no notification.
	}
}

// TestKernelCancel_StopsLongRunningHandler exercises SPEC §6.23
// per-request cancellation: kernel.cancel({request_id}) aborts an
// in-flight handler that honors ctx.Done(). We register a test-only
// "test.sleep" method that blocks on ctx; the client calls it in
// one goroutine and kernel.cancel in another. The blocked call
// must return promptly with the cancellation error.
func TestKernelCancel_StopsLongRunningHandler(t *testing.T) {
	t.Parallel()
	srv, _, sockPath := startTestServer(t)
	defer srv.Stop()

	// Register a long-running test-only method. The handler blocks
	// until either (a) ctx.Done() fires or (b) the per-call deadline
	// elapses; on cancel it returns ctx.Err() which the jsonrpc2
	// dispatch surfaces as a JSON-RPC error to the client.
	srv.AddCapability("test")
	Register(srv, "test.sleep", "test", func(ctx context.Context, _ struct{}) (struct{}, error) {
		select {
		case <-ctx.Done():
			return struct{}{}, ctx.Err()
		case <-time.After(30 * time.Second):
			return struct{}{}, nil
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	raw, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	stream := jsonrpc2.NewBufferedStream(raw, jsonrpc2.VSCodeObjectCodec{})
	conn := jsonrpc2.NewConn(ctx, stream, nil)
	defer func() { _ = conn.Close() }()

	// Issue the long-running call asynchronously so we can cancel it.
	type result struct {
		err     error
		elapsed time.Duration
	}
	resultC := make(chan result, 1)
	go func() {
		t0 := time.Now()
		// We don't know the jsonrpc2 ID ahead of time, but the
		// jsonrpc2 client allocates sequential numeric ids starting
		// at 0 for each conn. The fresh conn we just opened means
		// the first request gets id=0.
		err := conn.Call(ctx, "test.sleep", struct{}{}, nil)
		resultC <- result{err: err, elapsed: time.Since(t0)}
	}()

	// Give the server a beat to register the in-flight entry.
	time.Sleep(50 * time.Millisecond)

	// Cancel the in-flight request. JSON-RPC ids are numeric here
	// (the conn allocates them); the first request gets id=0.
	var cancelRes CancelResult
	if err := conn.Call(ctx, "kernel.cancel", CancelParams{RequestID: json.RawMessage("0")}, &cancelRes); err != nil {
		t.Fatalf("kernel.cancel call: %v", err)
	}
	if !cancelRes.Cancelled {
		// The cancel handler runs as its own request (id=1) so the
		// test.sleep request *should* still be the one with id=0.
		// If this assertion trips, the in-flight registry is wrong.
		t.Fatalf("kernel.cancel reported no match for request_id=0")
	}

	select {
	case r := <-resultC:
		if r.err == nil {
			t.Fatalf("cancelled call returned nil error; expected ctx-cancellation")
		}
		if r.elapsed > 2*time.Second {
			t.Fatalf("cancellation took too long: %v", r.elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("cancelled call did not return within deadline")
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
