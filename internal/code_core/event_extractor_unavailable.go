package code_core

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/shivamstaq/graph-harness/internal/source_live/detect"
)

// EventKindExtractorUnavailable is the bare event kind (without layer
// prefix) emitted when the orchestrator's detector reports a primary
// extractor is not present for an in-workspace language. Subscribers
// (TUI Doctor panel, change.process coverage_warning findings, MCP
// gh://doctor resource) all key off this kind.
const EventKindExtractorUnavailable = "ExtractorUnavailable"

// ExtractorUnavailablePayload is the JSON shape of the event. All
// fields are mirrored from a detect.ToolReport so consumers can
// reuse the same renderers as the doctor command.
type ExtractorUnavailablePayload struct {
	WorkspaceRoot string               `json:"workspace_root"`
	LanguageID    string               `json:"language_id"`
	ToolName      string               `json:"tool_name"`
	ToolClass     detect.ToolClass     `json:"tool_class"`
	ServerID      string               `json:"server_id,omitempty"`
	ProbeChain    []detect.ProbeStep   `json:"probe_chain"`
	InstallHints  []detect.InstallHint `json:"install_hints,omitempty"`
	DetectedAt    time.Time            `json:"detected_at"`
	IdempotencyKey string              `json:"idempotency_key"`
}

// MarshalExtractorUnavailable returns the JSON-encoded payload + the
// idempotency key the emitter uses to dedupe repeat detections during
// a single workspace open. Pure helper so tests can build the payload
// without going through an EventEmitter.
func MarshalExtractorUnavailable(workspaceRoot, languageID string, tool detect.ToolReport) ([]byte, string, error) {
	key := detect.IdempotencyKey(workspaceRoot, languageID, tool.Name)
	payload := ExtractorUnavailablePayload{
		WorkspaceRoot:  workspaceRoot,
		LanguageID:     languageID,
		ToolName:       tool.Name,
		ToolClass:      tool.Class,
		ServerID:       tool.ServerID,
		ProbeChain:     tool.ProbeChain,
		InstallHints:   tool.InstallHints,
		DetectedAt:     detect.Now().UTC(),
		IdempotencyKey: key,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("marshal ExtractorUnavailable: %w", err)
	}
	return buf, key, nil
}

// ExtractorUnavailableEmitter wraps an EventEmitter with idempotency
// state. Construct one per workspace open; calls to Emit are no-ops
// when a payload with the same idempotency key has already been sent.
// Safe for concurrent use.
type ExtractorUnavailableEmitter struct {
	Emitter EventEmitter

	mu   sync.Mutex
	seen map[string]struct{}
}

// NewExtractorUnavailableEmitter constructs the wrapper.
func NewExtractorUnavailableEmitter(e EventEmitter) *ExtractorUnavailableEmitter {
	return &ExtractorUnavailableEmitter{
		Emitter: e,
		seen:    make(map[string]struct{}),
	}
}

// Emit publishes a code.core.ExtractorUnavailable event for tool unless
// an event with the same idempotency key has already been emitted. The
// underlying EventEmitter may be nil — in that case Emit silently
// discards the event (CLI paths that don't open the event log).
func (e *ExtractorUnavailableEmitter) Emit(ctx context.Context, workspaceRoot, languageID string, tool detect.ToolReport) error {
	if e == nil || e.Emitter == nil {
		return nil
	}
	if tool.Status != detect.StatusMissing {
		return nil
	}
	buf, key, err := MarshalExtractorUnavailable(workspaceRoot, languageID, tool)
	if err != nil {
		return err
	}
	e.mu.Lock()
	if _, ok := e.seen[key]; ok {
		e.mu.Unlock()
		return nil
	}
	e.seen[key] = struct{}{}
	e.mu.Unlock()
	return e.Emitter.EmitCodeCoreEvent(ctx, EventKindExtractorUnavailable, buf)
}

// Reset clears the idempotency cache. Used by the daemon when a fresh
// workspace open should re-emit unchanged-but-still-missing events.
func (e *ExtractorUnavailableEmitter) Reset() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.seen = make(map[string]struct{})
	e.mu.Unlock()
}
