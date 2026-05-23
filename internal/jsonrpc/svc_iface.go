package jsonrpc

import (
	"context"

	"github.com/shivamstaq/graph-harness/internal/change_process"
	"github.com/shivamstaq/graph-harness/internal/review_queue"
	"github.com/shivamstaq/graph-harness/internal/semantic_overlay"
)

// Consumer is the surface TUI / Studio / MCP / repl consume from the
// daemon. Both `*Service` (in-process) and `*ClientService` (JSON-RPC
// remote) satisfy it; consumers depend on the interface so the
// CLI-startup `ResolveRoute` decider can hand them either flavor
// without forking the consumer's internals.
//
// Per F2 / plan/answers/04 §5: when a daemon is running, consumers
// MUST route through it as JSON-RPC clients rather than open SQLite
// handles directly. This interface is the seam the refactor pivots
// around — adding a method here means adding both Service-side
// behavior and ClientService-side dispatch in one place.
//
// Compile-time assertions at the bottom of this file lock in that
// both implementations satisfy the contract.
type Consumer interface {
	Status(ctx context.Context) (StatusResult, error)
	SelectorsTest(ctx context.Context, p SelectorsTestParams) (*semantic_overlay.ResolutionEnvelope, error)
	SelectorsPreview(ctx context.Context, p SelectorsPreviewParams) (SelectorsPreviewResult, error)
	ConflictsList(ctx context.Context) (ConflictsListResult, error)
	DoctorReport(ctx context.Context) (DoctorReportResult, error)
	OverlaySave(ctx context.Context, p OverlaySaveParams) (OverlaySaveResult, error)
	EntityProvenance(ctx context.Context, p EntityProvenanceParams) (EntityProvenanceResult, error)
	ValidateDiff(ctx context.Context, p ValidateDiffParams) (*change_process.ValidateDiffResult, error)
	FlowsList(ctx context.Context) (FlowsListResult, error)
	QueryParse(ctx context.Context, p QueryParseParams) (QueryParseResult, error)
	MCPBeforeEdit(ctx context.Context, p MCPBeforeEditParams) (MCPBeforeEditResult, error)
	MCPAfterEdit(ctx context.Context, p MCPAfterEditParams) (*change_process.ValidateDiffResult, error)
	ImpactedFlows(ctx context.Context, p ImpactedFlowsParams) (ImpactedFlowsResult, error)
	ReviewList(ctx context.Context, p ReviewListParams) (ReviewListResult, error)
	ReviewGet(ctx context.Context, p ReviewIDParams) (*review_queue.Proposal, error)
}

// ClientService adapts a *Client to the Consumer interface so
// remote daemon access is signature-compatible with in-process
// *Service. Each method is a thin Call wrapper.
type ClientService struct {
	c *Client
}

// NewClientService wraps a connected *Client as a Consumer.
func NewClientService(c *Client) *ClientService { return &ClientService{c: c} }

// Status — daemon `status` RPC.
func (s *ClientService) Status(ctx context.Context) (StatusResult, error) {
	var r StatusResult
	return r, s.c.Call(ctx, "status", nil, &r)
}

// SelectorsTest — daemon `selectors.test` RPC.
func (s *ClientService) SelectorsTest(ctx context.Context, p SelectorsTestParams) (*semantic_overlay.ResolutionEnvelope, error) {
	var r *semantic_overlay.ResolutionEnvelope
	return r, s.c.Call(ctx, "selectors.test", p, &r)
}

// SelectorsPreview — daemon `selectors.preview` RPC.
func (s *ClientService) SelectorsPreview(ctx context.Context, p SelectorsPreviewParams) (SelectorsPreviewResult, error) {
	var r SelectorsPreviewResult
	return r, s.c.Call(ctx, "selectors.preview", p, &r)
}

// ConflictsList — daemon `conflicts.list` RPC.
func (s *ClientService) ConflictsList(ctx context.Context) (ConflictsListResult, error) {
	var r ConflictsListResult
	return r, s.c.Call(ctx, "conflicts.list", nil, &r)
}

// DoctorReport — daemon `health.extractors` RPC.
func (s *ClientService) DoctorReport(ctx context.Context) (DoctorReportResult, error) {
	var r DoctorReportResult
	return r, s.c.Call(ctx, "health.extractors", nil, &r)
}

// OverlaySave — daemon `overlay.save` RPC.
func (s *ClientService) OverlaySave(ctx context.Context, p OverlaySaveParams) (OverlaySaveResult, error) {
	var r OverlaySaveResult
	return r, s.c.Call(ctx, "overlay.save", p, &r)
}

// EntityProvenance — daemon `entity.provenance` RPC.
func (s *ClientService) EntityProvenance(ctx context.Context, p EntityProvenanceParams) (EntityProvenanceResult, error) {
	var r EntityProvenanceResult
	return r, s.c.Call(ctx, "entity.provenance", p, &r)
}

// ValidateDiff — daemon `validate.diff` RPC.
func (s *ClientService) ValidateDiff(ctx context.Context, p ValidateDiffParams) (*change_process.ValidateDiffResult, error) {
	var r *change_process.ValidateDiffResult
	return r, s.c.Call(ctx, "validate.diff", p, &r)
}

// FlowsList — daemon `flows.list` RPC.
func (s *ClientService) FlowsList(ctx context.Context) (FlowsListResult, error) {
	var r FlowsListResult
	return r, s.c.Call(ctx, "flows.list", nil, &r)
}

// QueryParse — daemon `query.parse` RPC.
func (s *ClientService) QueryParse(ctx context.Context, p QueryParseParams) (QueryParseResult, error) {
	var r QueryParseResult
	return r, s.c.Call(ctx, "query.parse", p, &r)
}

// MCPBeforeEdit — daemon `mcp.before_edit` RPC.
func (s *ClientService) MCPBeforeEdit(ctx context.Context, p MCPBeforeEditParams) (MCPBeforeEditResult, error) {
	var r MCPBeforeEditResult
	return r, s.c.Call(ctx, "mcp.before_edit", p, &r)
}

// MCPAfterEdit — daemon `mcp.after_edit` RPC.
func (s *ClientService) MCPAfterEdit(ctx context.Context, p MCPAfterEditParams) (*change_process.ValidateDiffResult, error) {
	var r *change_process.ValidateDiffResult
	return r, s.c.Call(ctx, "mcp.after_edit", p, &r)
}

// ImpactedFlows — daemon `mcp.impacted_flows` RPC (P2.T41).
func (s *ClientService) ImpactedFlows(ctx context.Context, p ImpactedFlowsParams) (ImpactedFlowsResult, error) {
	var r ImpactedFlowsResult
	return r, s.c.Call(ctx, "mcp.impacted_flows", p, &r)
}

// ReviewList — daemon `review.list` RPC.
func (s *ClientService) ReviewList(ctx context.Context, p ReviewListParams) (ReviewListResult, error) {
	var r ReviewListResult
	return r, s.c.Call(ctx, "review.list", p, &r)
}

// ReviewGet — daemon `review.get` RPC.
func (s *ClientService) ReviewGet(ctx context.Context, p ReviewIDParams) (*review_queue.Proposal, error) {
	var r *review_queue.Proposal
	return r, s.c.Call(ctx, "review.get", p, &r)
}

// Compile-time assertions: both backends satisfy the Consumer
// contract. Adding a method to Consumer requires adding it on both.
var (
	_ Consumer = (*Service)(nil)
	_ Consumer = (*ClientService)(nil)
)
