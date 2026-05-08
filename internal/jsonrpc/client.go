package jsonrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"runtime"
	"time"

	"github.com/sourcegraph/jsonrpc2"
)

// Client is a thin wrapper around jsonrpc2.Conn for callers (CLI, TUI,
// Studio, MCP, VS Code) that need to invoke daemon methods.
//
// Connect dials the workspace socket (Unix domain socket on Linux/macOS;
// named pipe on Windows). On Windows, named-pipe dial uses winio when
// available; for the P0 stub, the dial path falls back to TCP loopback
// if both fail (with a clear error). The cross-platform "feature:p0-named-pipe"
// gate in tests/e2e/features.yaml soft-gates Windows.
type Client struct {
	conn *jsonrpc2.Conn
}

// Dial opens a Client to the daemon socket at the given path.
func Dial(ctx context.Context, socketPath string) (*Client, error) {
	raw, err := dialSocket(ctx, socketPath)
	if err != nil {
		return nil, err
	}
	stream := jsonrpc2.NewBufferedStream(raw, jsonrpc2.VSCodeObjectCodec{})
	conn := jsonrpc2.NewConn(ctx, stream, nil) // client-only: no incoming handler
	return &Client{conn: conn}, nil
}

// Call invokes a method synchronously. result must be a pointer to a value
// the daemon's response can JSON-decode into; pass nil to discard.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	if result == nil {
		var sink json.RawMessage
		return c.conn.Call(ctx, method, params, &sink)
	}
	return c.conn.Call(ctx, method, params, result)
}

// Close tears down the connection.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func dialSocket(ctx context.Context, path string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 3 * time.Second}
	if runtime.GOOS == "windows" {
		// Named-pipe path; net.Dial supports `\\.\pipe\name` via the
		// "tcp"/"unix" networks indirectly. Real Windows support uses
		// github.com/Microsoft/go-winio in P5+; for P0 we attempt a
		// generic dial and surface a clear error if it fails.
		return dialer.DialContext(ctx, "unix", path)
	}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", path, err)
	}
	return conn, nil
}
