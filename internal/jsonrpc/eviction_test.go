package jsonrpc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/sourcegraph/jsonrpc2"
)

// TestSubscriptionManager_EvictsIdleConns asserts the SPEC §6.22
// heartbeat eviction clause: a conn whose lastSeen exceeds the
// idle threshold is dropped, its subscriptions are torn down, and
// a SubscriberEvicted event lands on the kernel bus so monitoring
// consumers learn about the loss.
func TestSubscriptionManager_EvictsIdleConns(t *testing.T) {
	t.Parallel()
	srv, log, sockPath := startTestServer(t)
	defer srv.Stop()
	srv.svc.subscriptions().SetIdleThreshold(50 * time.Millisecond)

	raw, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	stream := jsonrpc2.NewBufferedStream(raw, jsonrpc2.VSCodeObjectCodec{})
	conn := jsonrpc2.NewConn(context.Background(), stream, nil)
	defer func() { _ = conn.Close() }()

	var ack IdentifyResult
	if err := conn.Call(context.Background(), "kernel.identify",
		IdentifyParams{SubscriberID: "evict-victim"}, &ack); err != nil {
		t.Fatalf("identify: %v", err)
	}
	var subResult SubscribeResult
	if err := conn.Call(context.Background(), "kernel.subscribe",
		SubscribeParams{Filter: "code.core"}, &subResult); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Don't touch the conn for longer than the threshold, then
	// drive the eviction sweep manually (avoids racing the ticker).
	time.Sleep(80 * time.Millisecond)
	srv.svc.subscriptions().evictIdleConns()

	events, err := log.ReadAll(context.Background())
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	found := false
	for _, ev := range events {
		if ev.Layer == "kernel.subscriptions" && ev.Kind == "SubscriberEvicted" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("SubscriberEvicted not emitted; events=%+v", events)
	}
}

// TestSubscriptionManager_TouchKeepsConnAlive asserts the inverse:
// regular kernel.ping refreshes lastSeen so an active conn isn't
// evicted across sleeps longer than the threshold.
func TestSubscriptionManager_TouchKeepsConnAlive(t *testing.T) {
	t.Parallel()
	srv, log, sockPath := startTestServer(t)
	defer srv.Stop()
	srv.svc.subscriptions().SetIdleThreshold(80 * time.Millisecond)

	raw, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	stream := jsonrpc2.NewBufferedStream(raw, jsonrpc2.VSCodeObjectCodec{})
	conn := jsonrpc2.NewConn(context.Background(), stream, nil)
	defer func() { _ = conn.Close() }()

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		var pong KernelPingResult
		if err := conn.Call(context.Background(), "kernel.ping", nil, &pong); err != nil {
			t.Fatalf("ping: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	srv.svc.subscriptions().evictIdleConns()

	events, _ := log.ReadAll(context.Background())
	for _, ev := range events {
		if ev.Kind == "SubscriberEvicted" {
			t.Errorf("conn evicted despite regular pings: %+v", ev)
		}
	}
}
