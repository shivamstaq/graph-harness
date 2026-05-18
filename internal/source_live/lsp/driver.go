package lsp

import (
	"context"
	"encoding/json"

	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// NotificationHandler receives server-initiated LSP notifications
// (textDocument/publishDiagnostics, $/progress, documentSymbol push
// variants where the server supports them). Handlers run on the
// driver's read goroutine, so heavy work must be dispatched to a
// separate goroutine to avoid blocking the LSP transport. P1.5.T02.
type NotificationHandler func(languageID, method string, params json.RawMessage)

// Location is a workspace-relative path + range, returned by Definition
// and References. We keep the LSP wire types internal and project them
// to the source.live envelope here so callers don't need to know the
// transport.
type Location struct {
	Path  string
	Range source_live.Range
}

// Driver is the language-agnostic interface every LSP backend
// implements. Drivers are owned by Host; callers should resolve a
// Driver via Host.DriverFor(language) rather than instantiate one
// directly so the lifecycle (lazy spawn, restart) stays centralized.
//
// Methods are safe to call from multiple goroutines once Initialize
// has returned.
type Driver interface {
	// Language returns the canonical language id this driver serves
	// (e.g. "go", "typescript", "python").
	Language() string

	// Initialize sends `initialize` + `initialized` to the underlying
	// server and registers the workspace root. Idempotent on a single
	// Driver instance — repeated calls are no-ops once the server has
	// transitioned to Ready.
	Initialize(ctx context.Context, root string) error

	// DocumentSymbol fetches symbol facts for a single file. Path is
	// workspace-relative; the driver translates it to a file:// URI.
	// Returns Symbols with SourceClass=SourceClassLSP and ProducedBy
	// set to the driver-specific extractor id.
	DocumentSymbol(ctx context.Context, path string) ([]source_live.Symbol, error)

	// Definition resolves the definition site of the symbol at the
	// given file/line/column. Used by code.core to cross-link
	// references → definitions.
	Definition(ctx context.Context, path string, line, character uint32) ([]Location, error)

	// References resolves the reference sites of the symbol at the
	// given location. include_declaration follows the LSP convention.
	References(ctx context.Context, path string, line, character uint32, includeDeclaration bool) ([]Location, error)

	// Health pings the server with a cheap request to confirm
	// liveness. Returns nil on a healthy server, a wrapped error if
	// the subprocess has died or stopped responding.
	Health(ctx context.Context) error

	// Shutdown sends `shutdown` + `exit` and reaps the subprocess.
	// Idempotent; safe to call from a deferred context.
	Shutdown(ctx context.Context) error

	// SetNotificationHandler installs a callback that receives every
	// server-initiated LSP notification (publishDiagnostics, $/progress,
	// documentSymbol push). Must be called before Initialize or its
	// effect is delayed until the next Initialize. Idempotent: passing
	// nil clears the handler. P1.5.T02 — the daemon installs a handler
	// that forwards into the kernel bus so push-back from gopls /
	// tsserver / pyright surfaces as code.core drift events.
	SetNotificationHandler(h NotificationHandler)
}
