package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// pipePair connects an in-memory io.Pipe pair for the test transport.
func pipePair() (clientWriter io.WriteCloser, clientReader io.ReadCloser, serverWriter io.WriteCloser, serverReader io.ReadCloser) {
	cR, sW := io.Pipe()
	sR, cW := io.Pipe()
	return cW, cR, sW, sR
}

// fakeServer reads framed JSON-RPC messages, handles `echo` by
// returning the request params as the response result, and exits
// cleanly when its read side returns EOF.
func fakeServer(t *testing.T, w io.Writer, r io.Reader, ready chan<- struct{}) {
	t.Helper()
	br := bufio.NewReader(r)
	close(ready)
	for {
		clen, err := readContentLength(br)
		if err != nil {
			return
		}
		body := make([]byte, clen)
		if _, err := io.ReadFull(br, body); err != nil {
			return
		}
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("server: bad request: %v", err)
			return
		}
		if req.Method != "echo" {
			continue
		}
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  req.Params,
		}
		raw, _ := json.Marshal(resp)
		header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(raw))
		if _, err := io.WriteString(w, header); err != nil {
			return
		}
		if _, err := w.Write(raw); err != nil {
			return
		}
	}
}

func TestJSONRPC_RoundTrip(t *testing.T) {
	cW, cR, sW, sR := pipePair()
	t.Cleanup(func() { _ = cW.Close(); _ = cR.Close(); _ = sW.Close(); _ = sR.Close() })

	ready := make(chan struct{})
	go fakeServer(t, sW, sR, ready)
	<-ready

	conn := newJSONRPC(cW, cR, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	go conn.serve(ctx)

	res, err := conn.Call(ctx, "echo", map[string]any{"hello": "world"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(res, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["hello"] != "world" {
		t.Errorf("echo result = %v, want hello=world", got)
	}
}

func TestJSONRPC_NotificationDispatched(t *testing.T) {
	var buf bytes.Buffer
	rPipeR, rPipeW := io.Pipe()
	t.Cleanup(func() { _ = rPipeR.Close(); _ = rPipeW.Close() })

	gotMethod := make(chan string, 1)
	conn := newJSONRPC(&buf, rPipeR, func(method string, params json.RawMessage) {
		gotMethod <- method
		_ = params
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go conn.serve(ctx)

	notif := `{"jsonrpc":"2.0","method":"window/logMessage","params":{"type":1,"message":"hi"}}`
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(notif))
	go func() {
		_, _ = io.WriteString(rPipeW, header+notif)
	}()

	select {
	case got := <-gotMethod:
		if got != "window/logMessage" {
			t.Errorf("notif method = %q, want window/logMessage", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification never delivered")
	}
}

func TestJSONRPC_ContentLengthHeader(t *testing.T) {
	br := bufio.NewReader(strings.NewReader("Content-Length: 42\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\n\r\n"))
	n, err := readContentLength(br)
	if err != nil {
		t.Fatalf("readContentLength: %v", err)
	}
	if n != 42 {
		t.Errorf("Content-Length = %d, want 42", n)
	}
}

func TestJSONRPC_FailPendingOnReadError(t *testing.T) {
	rPipeR, rPipeW := io.Pipe()
	conn := newJSONRPC(io.Discard, rPipeR, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	go conn.serve(ctx)

	var wg sync.WaitGroup
	wg.Add(1)
	var callErr error
	go func() {
		defer wg.Done()
		_, callErr = conn.Call(ctx, "ping", nil)
	}()
	// Force an EOF to fail the pending Call.
	_ = rPipeW.Close()
	wg.Wait()
	if callErr == nil {
		t.Errorf("expected error when underlying read fails, got nil")
	}
}
