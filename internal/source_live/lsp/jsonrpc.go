package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// jsonrpcConn implements the LSP base protocol (JSON-RPC 2.0 framed by
// Content-Length headers) over a stream-pair. It does not depend on
// any external LSP library; the message subset we need is small and
// stable.
//
// Concurrency: Call may be invoked from multiple goroutines safely.
// The reader goroutine is launched in serve and exits when r returns
// EOF or ctx is canceled.
type jsonrpcConn struct {
	w  io.Writer
	r  *bufio.Reader
	id atomic.Int64

	mu       sync.Mutex
	pending  map[int64]chan *rpcResponse
	notif    func(method string, params json.RawMessage)
	closed   bool
	closeErr error
	stopped  chan struct{}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("lsp: %d %s", e.Code, e.Message)
}

func newJSONRPC(w io.Writer, r io.Reader, notif func(method string, params json.RawMessage)) *jsonrpcConn {
	c := &jsonrpcConn{
		w:       w,
		r:       bufio.NewReader(r),
		pending: make(map[int64]chan *rpcResponse),
		notif:   notif,
		stopped: make(chan struct{}),
	}
	return c
}

// serve runs the read loop until EOF or ctx cancellation.
func (c *jsonrpcConn) serve(ctx context.Context) {
	defer close(c.stopped)
	for {
		select {
		case <-ctx.Done():
			c.failPending(ctx.Err())
			return
		default:
		}
		msg, err := c.readMessage()
		if err != nil {
			c.failPending(err)
			return
		}
		if msg.ID != nil && (msg.Result != nil || msg.Error != nil) {
			c.deliverResponse(msg)
			continue
		}
		if c.notif != nil && msg.Method != "" {
			c.notif(msg.Method, msg.Params)
		}
	}
}

func (c *jsonrpcConn) deliverResponse(msg *rpcResponse) {
	c.mu.Lock()
	ch, ok := c.pending[*msg.ID]
	if ok {
		delete(c.pending, *msg.ID)
	}
	c.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

func (c *jsonrpcConn) failPending(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.closeErr = err
	for id, ch := range c.pending {
		select {
		case ch <- &rpcResponse{Error: &rpcError{Code: -32000, Message: err.Error()}}:
		default:
		}
		delete(c.pending, id)
	}
}

// Call sends a request and waits for its response. The caller is
// expected to JSON-unmarshal Result themselves; we keep it as
// json.RawMessage so the transport stays type-agnostic.
func (c *jsonrpcConn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.id.Add(1)
	ch := make(chan *rpcResponse, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("jsonrpc: connection closed: %w", c.closeErr)
	}
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.writeMessage(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// Notify sends a notification (no response expected).
func (c *jsonrpcConn) Notify(method string, params any) error {
	return c.writeMessage(rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *jsonrpcConn) writeMessage(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("jsonrpc: connection closed: %w", c.closeErr)
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(c.w, header); err != nil {
		return err
	}
	if _, err := c.w.Write(body); err != nil {
		return err
	}
	return nil
}

func (c *jsonrpcConn) readMessage() (*rpcResponse, error) {
	contentLen, err := readContentLength(c.r)
	if err != nil {
		return nil, err
	}
	body := make([]byte, contentLen)
	if _, err := io.ReadFull(c.r, body); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	msg := &rpcResponse{}
	if err := json.Unmarshal(body, msg); err != nil {
		return nil, fmt.Errorf("unmarshal: %w (body=%q)", err, string(body))
	}
	return msg, nil
}

// readContentLength reads LSP base-protocol headers and returns the
// Content-Length value. Other headers (Content-Type) are tolerated and
// ignored.
func readContentLength(r *bufio.Reader) (int, error) {
	var contentLen int
	var sawLength bool
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return 0, fmt.Errorf("invalid Content-Length: %w", err)
			}
			contentLen = n
			sawLength = true
		}
	}
	if !sawLength {
		return 0, errors.New("missing Content-Length header")
	}
	return contentLen, nil
}
