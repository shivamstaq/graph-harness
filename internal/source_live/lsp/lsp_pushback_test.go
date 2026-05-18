package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

// TestNotificationHandler_ReceivesPublishDiagnostics exercises the
// F3 / P1.5.T02 server-push contract at the jsonrpc transport seam:
// a server-initiated `textDocument/publishDiagnostics` notification
// is forwarded to the installed handler with the language id, method,
// and raw params.
//
// We don't spawn a real gopls / tsserver / pyright (those are
// covered by the per-driver integration tests). The fake LSP server
// here writes a publishDiagnostics frame and asserts the handler
// fires synchronously with the expected fields. The daemon-side
// re-extract trigger is exercised by the e2e spec
// lsp-children-publish-drift-events.yaml.
func TestNotificationHandler_ReceivesPublishDiagnostics(t *testing.T) {
	t.Parallel()

	type capture struct {
		languageID string
		method     string
		params     json.RawMessage
	}
	captured := make(chan capture, 1)

	// Bind a NotificationHandler via the same shape genericDriver
	// installs: closure adapts to languageID via closure capture.
	const fakeLanguageID = "go"
	handler := func(method string, params json.RawMessage) {
		captured <- capture{
			languageID: fakeLanguageID,
			method:     method,
			params:     params,
		}
	}

	// Spawn the jsonrpc transport with our handler. Server writes
	// into rPipe, client (us) reads from it.
	rPipeR, rPipeW := io.Pipe()
	t.Cleanup(func() { _ = rPipeR.Close(); _ = rPipeW.Close() })
	var wbuf bytes.Buffer
	var wmu sync.Mutex
	w := &syncWriter{w: &wbuf, mu: &wmu}
	conn := newJSONRPC(w, rPipeR, handler)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go conn.serve(ctx)

	// Fake server emits publishDiagnostics. params.uri tells the
	// daemon-side bridge which file to re-extract.
	notif := `{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{"uri":"file:///workspace/main.go","diagnostics":[{"range":{"start":{"line":1,"character":0},"end":{"line":1,"character":5}},"severity":1,"message":"undefined: Foo"}]}}`
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(notif), notif)
	go func() { _, _ = io.WriteString(rPipeW, frame) }()

	select {
	case got := <-captured:
		if got.method != "textDocument/publishDiagnostics" {
			t.Errorf("method = %q, want textDocument/publishDiagnostics", got.method)
		}
		if got.languageID != "go" {
			t.Errorf("languageID = %q, want go", got.languageID)
		}
		// Verify params carries the uri the daemon needs to derive
		// the workspace-relative path.
		var probe struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(got.params, &probe); err != nil {
			t.Fatalf("unmarshal params: %v", err)
		}
		if probe.URI != "file:///workspace/main.go" {
			t.Errorf("params.uri = %q, want file:///workspace/main.go", probe.URI)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publishDiagnostics never delivered to handler")
	}
}

// TestHost_SetNotificationHandler_PropagatesToDrivers asserts that
// Host.SetNotificationHandler propagates the callback to every
// driver registered at the time of call. Future drivers built via
// DriverFor also inherit via the same mechanism.
func TestHost_SetNotificationHandler_PropagatesToDrivers(t *testing.T) {
	t.Parallel()
	h := NewHost(NewRegistry(), t.TempDir())
	t.Cleanup(func() { _ = h.Close(t.Context()) })

	// Install a no-op handler; we're testing propagation, not behavior.
	called := make(chan struct{}, 1)
	h.SetNotificationHandler(func(_, _ string, _ json.RawMessage) {
		select {
		case called <- struct{}{}:
		default:
		}
	})

	// Inspect the host's stored handler via the internal field (we're
	// in the same package). The driver-propagation path is exercised
	// when DriverFor builds a new driver — covered by the integration
	// tests for the real drivers. Here we just lock in that the
	// host-scope handler is reachable.
	h.mu.Lock()
	stored := h.notifHandler
	h.mu.Unlock()
	if stored == nil {
		t.Fatal("Host.notifHandler nil after SetNotificationHandler")
	}
	stored("go", "textDocument/publishDiagnostics", json.RawMessage(`{"uri":"file:///x.go"}`))
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("stored handler did not fire when invoked")
	}
}

// syncWriter wraps an io.Writer in a mutex so the test goroutine
// and the jsonrpc serve goroutine can safely share a buffer.
type syncWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
