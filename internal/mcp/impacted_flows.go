package mcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
)

// impactedFlowsToolDescriptor is the tools/list descriptor for
// `impacted_flows`. Two-prong input: either `diff` (unified diff blob)
// OR `selector` (named selector from the workspace overlay). Exactly
// one must be set. (P2.T41)
func impactedFlowsToolDescriptor() ToolDescriptor {
	return ToolDescriptor{
		Name: "impacted_flows",
		Description: "Project a diff (or a named selector) to the framework impacted set and list every flow whose scope contains an impacted entity. " +
			"Evidence carries the producer + dependents for each touched framework edge.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"diff": map[string]any{
					"type":        "string",
					"description": "Unified-diff blob — projects through code.core lookups, framework-edge BFS, then flow scope membership.",
				},
				"selector": map[string]any{
					"type":        "string",
					"description": "Name of a selector from the workspace overlay. Resolved → entity IDs → same BFS as diff.",
				},
			},
			// MCP does not standardize oneOf-style validation, so the
			// adapter handler enforces exactly-one at dispatch time.
		},
	}
}

// impactedFlowsHandler is the tools/call handler. Validates exactly-one
// of {diff, selector}, then delegates to Consumer.ImpactedFlows so the
// Stage 4-5 BFS reused from the change_process pipeline stays
// single-sourced.
func impactedFlowsHandler(a *Adapter) func(ctx context.Context, args json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var p struct {
			Diff     string `json:"diff,omitempty"`
			Selector string `json:"selector,omitempty"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, err
		}
		if (p.Diff == "") == (p.Selector == "") {
			return nil, errors.New("impacted_flows: exactly one of diff or selector required")
		}
		svc, err := a.service()
		if err != nil {
			return nil, err
		}
		return svc.ImpactedFlows(ctx, jsonrpc.ImpactedFlowsParams{
			Diff:     p.Diff,
			Selector: p.Selector,
		})
	}
}
