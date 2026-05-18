package jsonrpc

import (
	"context"
	"net"
	"testing"

	"github.com/sourcegraph/jsonrpc2"
)

// TestKernelPing_RoundTrips asserts the SPEC §6.22 heartbeat clause:
// kernel.ping returns {pong: true} so long-lived consumers (TUI,
// Studio, IDE) can probe their daemon connection on an idle timer
// and reconnect when the round-trip fails. The wire format is the
// fixed integration point — clients across language boundaries
// (Go TUI, TypeScript VS Code extension, Python MCP probe) all
// depend on the literal `"pong": true` boolean.
func TestKernelPing_RoundTrips(t *testing.T) {
	t.Parallel()
	srv, _, sockPath := startTestServer(t)
	defer srv.Stop()

	raw, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	stream := jsonrpc2.NewBufferedStream(raw, jsonrpc2.VSCodeObjectCodec{})
	conn := jsonrpc2.NewConn(context.Background(), stream, nil)
	defer func() { _ = conn.Close() }()

	var out KernelPingResult
	if err := conn.Call(context.Background(), "kernel.ping", nil, &out); err != nil {
		t.Fatalf("kernel.ping: %v", err)
	}
	if !out.Pong {
		t.Errorf("kernel.ping: want {pong:true}, got %+v", out)
	}
}
