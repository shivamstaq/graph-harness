package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"

	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
)

// Adapter is the MCP server (server-role; the agent is the client). It speaks
// MCP over stdio in line-delimited JSON-RPC 2.0 (newline-delimited JSON
// messages, one request/response per line). The agent invokes:
//
//	initialize, tools/list, tools/call, resources/list, resources/read,
//	prompts/list, prompts/get, notifications/initialized
//
// Each tool call is delegated through the jsonrpc.Consumer interface
// so the adapter stays a thin curated façade — there is no second
// protocol or second copy of business logic. Per F2 / plan/answers/04
// §5, the concrete Consumer may be either *jsonrpc.Service (in-process)
// or *jsonrpc.ClientService (JSON-RPC remote when daemon is running).
type Adapter struct {
	svc     jsonrpc.Consumer
	openSvc func() (jsonrpc.Consumer, error)

	mu    sync.Mutex
	tools map[string]toolHandler
	res   map[string]resourceHandler

	// entityResourcePrefix + entityResourceDesc back the dynamic
	// gh://entity/code.core/<kind>/<id> resource (P1.T38). resources/list
	// reports the templated descriptor; resources/read matches incoming
	// URIs by prefix and parses the remainder into kind + entity_id.
	entityResourcePrefix string
	entityResourceDesc   ResourceDescriptor

	// frameworkResourcePrefix + frameworkResourceDesc back the dynamic
	// gh://framework/<extractor>/<entity> resource (P2.T41). The tail
	// after the prefix is split into <extractor>/<entity>; the
	// <entity> segment is URL-encoded "<Kind>:<QualifiedName>" so an
	// entity like `Route:GET /api/orders` round-trips safely. The
	// extractor name (e.g. "routes.go.chi") is matched against the
	// provenance ProducedBy field after the entity is resolved.
	frameworkResourcePrefix string
	frameworkResourceDesc   ResourceDescriptor
}

// service returns the live Consumer. With a lazy adapter, the first
// caller pays the workspace-open cost; subsequent callers reuse the
// cached handle.
func (a *Adapter) service() (jsonrpc.Consumer, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.svc != nil {
		return a.svc, nil
	}
	if a.openSvc == nil {
		return nil, errors.New("mcp adapter has no service bound")
	}
	svc, err := a.openSvc()
	if err != nil {
		return nil, err
	}
	a.svc = svc
	return svc, nil
}

type toolHandler struct {
	desc   ToolDescriptor
	handle func(ctx context.Context, args json.RawMessage) (any, error)
}

type resourceHandler struct {
	desc   ResourceDescriptor
	handle func(ctx context.Context) (any, error)
}

// ToolDescriptor is the wire shape returned by tools/list.
type ToolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ResourceDescriptor is the wire shape returned by resources/list.
type ResourceDescriptor struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType,omitempty"`
}

// NewAdapter builds an adapter wired to the given Consumer.
func NewAdapter(svc jsonrpc.Consumer) *Adapter {
	a := &Adapter{
		svc:   svc,
		tools: map[string]toolHandler{},
		res:   map[string]resourceHandler{},
	}
	a.registerTools()
	a.registerResources()
	return a
}

// NewLazyAdapter builds an adapter that fetches the underlying service on
// the first tool/resource call. Static surface methods (tools/list,
// resources/list, initialize, prompts/list, prompts/get) succeed regardless
// of workspace state; tools/call + resources/read invoke openSvc and
// surface its error as a structured tool result.
//
// This shape lets `graph-harness mcp` advertise its surface in any cwd (so
// agents can introspect what's available) while still requiring an
// initialized workspace for actual graph operations.
func NewLazyAdapter(openSvc func() (jsonrpc.Consumer, error)) *Adapter {
	// Register descriptors against a sentinel service that errors on every
	// call; the dispatcher swaps in the real service via openSvc on first
	// tools/call.
	a := &Adapter{
		tools:   map[string]toolHandler{},
		res:     map[string]resourceHandler{},
		openSvc: openSvc,
	}
	// Build a placeholder Adapter to enumerate tool descriptors; the
	// handlers are then re-bound to a getSvc-wrapped form so they always
	// see the live service.
	// Handlers in registerTools/registerResources call a.service() so they
	// pick up the lazily-opened service on first invocation; the
	// descriptors (Name/Description/InputSchema) bind immediately.
	a.registerTools()
	a.registerResources()
	return a
}

// registerTools wires the curated v0 + P1 tool surface.
func (a *Adapter) registerTools() {
	stringSchema := func(prop string, desc string) map[string]any {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				prop: map[string]any{"type": "string", "description": desc},
			},
			"required": []string{prop},
		}
	}

	a.tools["explore"] = toolHandler{
		desc: ToolDescriptor{
			Name:        "explore",
			Description: "Resolve a named selector and report its bound entities + neighborhood.",
			InputSchema: stringSchema("selector", "name of a selector defined in the workspace overlay"),
		},
		handle: func(ctx context.Context, args json.RawMessage) (any, error) {
			var p struct {
				Selector string `json:"selector"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			svc, err := a.service()
			if err != nil {
				return nil, err
			}
			return svc.SelectorsTest(ctx, jsonrpc.SelectorsTestParams{Name: p.Selector})
		},
	}
	a.tools["validate_diff"] = toolHandler{
		desc: ToolDescriptor{
			Name:        "validate_diff",
			Description: "Run change.process against a unified diff and report findings.",
			InputSchema: stringSchema("diff", "unified diff (text)"),
		},
		handle: func(ctx context.Context, args json.RawMessage) (any, error) {
			var p struct {
				Diff string `json:"diff"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			svc, err := a.service()
			if err != nil {
				return nil, err
			}
			return svc.ValidateDiff(ctx, jsonrpc.ValidateDiffParams{Diff: p.Diff})
		},
	}
	a.tools["get_context"] = toolHandler{
		desc: ToolDescriptor{
			Name:        "get_context",
			Description: "Fetch flow / invariant / control evidence for a selector.",
			InputSchema: stringSchema("selector", "name of a selector or qualified_name to surface evidence for"),
		},
		handle: func(ctx context.Context, args json.RawMessage) (any, error) {
			var p struct {
				Selector string `json:"selector"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			svc, err := a.service()
			if err != nil {
				return nil, err
			}
			env, err := svc.SelectorsTest(ctx, jsonrpc.SelectorsTestParams{Name: p.Selector})
			if err != nil {
				return nil, err
			}
			flows, err := svc.FlowsList(ctx)
			if err != nil {
				return nil, err
			}
			// Surface flows whose declared scope matches the selector.
			scoped := make([]any, 0)
			for _, f := range flows.Flows {
				if f.Scope == p.Selector {
					scoped = append(scoped, f)
				}
			}
			return map[string]any{
				"selector":     p.Selector,
				"resolution":   env,
				"scoped_flows": scoped,
				"invariants":   []any{}, // populated in P3
				"controls":     []any{}, // populated in P3
				"resolved_at":  env.ResolvedAt,
			}, nil
		},
	}
	a.tools["query"] = toolHandler{
		desc: ToolDescriptor{
			Name:        "query",
			Description: "Parse a DSL source string and return its canonical AST envelope.",
			InputSchema: stringSchema("source", ".gh / DSL source"),
		},
		handle: func(ctx context.Context, args json.RawMessage) (any, error) {
			var p struct {
				Source string `json:"source"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			svc, err := a.service()
			if err != nil {
				return nil, err
			}
			return svc.QueryParse(ctx, jsonrpc.QueryParseParams{Source: p.Source})
		},
	}
	// P1.T38 — before_edit / after_edit
	a.tools["before_edit"] = toolHandler{
		desc: ToolDescriptor{
			Name:        "before_edit",
			Description: "Snapshot selector resolution + body-hash anchors before an edit.",
			InputSchema: stringSchema("selector", "selector the agent is about to edit"),
		},
		handle: func(ctx context.Context, args json.RawMessage) (any, error) {
			var p struct {
				Selector string `json:"selector"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			svc, err := a.service()
			if err != nil {
				return nil, err
			}
			return svc.MCPBeforeEdit(ctx, jsonrpc.MCPBeforeEditParams{Selector: p.Selector})
		},
	}
	a.tools["after_edit"] = toolHandler{
		desc: ToolDescriptor{
			Name:        "after_edit",
			Description: "Validate a unified diff produced by an edit and report findings.",
			InputSchema: stringSchema("diff", "unified diff produced after the edit"),
		},
		handle: func(ctx context.Context, args json.RawMessage) (any, error) {
			var p struct {
				Diff string `json:"diff"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			svc, err := a.service()
			if err != nil {
				return nil, err
			}
			return svc.MCPAfterEdit(ctx, jsonrpc.MCPAfterEditParams{Diff: p.Diff})
		},
	}
	// P2.T41 — impacted_flows
	a.tools["impacted_flows"] = toolHandler{
		desc:   impactedFlowsToolDescriptor(),
		handle: impactedFlowsHandler(a),
	}
}

// registerResources wires the resource surface.
func (a *Adapter) registerResources() {
	a.res["gh://workspace/status"] = resourceHandler{
		desc: ResourceDescriptor{
			URI:         "gh://workspace/status",
			Name:        "Workspace status",
			Description: "Current workspace + last_seq summary",
			MimeType:    "application/json",
		},
		handle: func(ctx context.Context) (any, error) {
			svc, err := a.service()
			if err != nil {
				return nil, err
			}
			return svc.Status(ctx)
		},
	}
	a.res["gh://doctor"] = resourceHandler{
		desc: ResourceDescriptor{
			URI:         "gh://doctor",
			Name:        "Tooling detection report",
			Description: "Per-language LSP / SCIP / parser availability with install hints (P1.L; SPEC §6.18). Mirrors `graph-harness doctor --json`.",
			MimeType:    "application/json",
		},
		handle: func(ctx context.Context) (any, error) {
			svc, err := a.service()
			if err != nil {
				return nil, err
			}
			return svc.DoctorReport(ctx)
		},
	}
	// gh://entity/code.core/<kind>/<id> — dynamic per-entity resource
	// (P1.T38). Static enumeration in resources/list reports a templated
	// example URI; resources/read matches by prefix and parses the
	// remainder into kind + entity_id, then drills via entity.provenance.
	a.entityResourcePrefix = "gh://entity/code.core/"
	a.entityResourceDesc = ResourceDescriptor{
		URI:         a.entityResourcePrefix + "{kind}/{entity_id}",
		Name:        "code.core entity provenance",
		Description: "Merged provenance (folded summary + per-source claims) for a code.core entity. Supply <kind>/<entity_id> in the URI.",
		MimeType:    "application/json",
	}

	// gh://framework/<extractor>/<entity> — dynamic per-framework-entity
	// resource (P2.T41). Tail layout: <extractor>/<urlencoded entity>.
	// The <entity> segment is URL-encoded "<Kind>:<QualifiedName>"
	// (e.g. "Route:GET%20%2Fapi%2Forders"). The server resolves entities
	// matching the QualifiedName via LookupAllByQualifiedName, filters
	// to the requested Kind + a provenance source whose ProducedBy
	// matches the extractor, then returns:
	//
	//	{ "entity": EntityView, "extractor": "<name>",
	//	  "bound_flows": []FlowImpactRef,
	//	  "findings":    []ValidationFinding }
	a.frameworkResourcePrefix = "gh://framework/"
	a.frameworkResourceDesc = ResourceDescriptor{
		URI:         a.frameworkResourcePrefix + "{extractor}/{kind}:{qualified_name}",
		Name:        "code.framework entity",
		Description: "Canonical code.framework entity payload + bound flows + active findings. URI: gh://framework/<extractor>/<urlencoded Kind:QualifiedName>.",
		MimeType:    "application/json",
	}
}

// --- protocol --------------------------------------------------------------

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResp struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Serve runs the MCP server over the given streams (stdio in production).
func (a *Adapter) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var req rpcReq
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(rpcResp{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: err.Error()}})
			continue
		}
		resp := a.dispatch(ctx, req)
		// Notifications (no id) get no reply.
		if req.ID == nil && req.Method != "" && strings.HasPrefix(req.Method, "notifications/") {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (a *Adapter) dispatch(ctx context.Context, req rpcReq) rpcResp {
	resp := rpcResp{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo": map[string]string{
				"name":    "graph-harness",
				"version": "0.1.0-dev",
			},
			"capabilities": map[string]any{
				"tools":     map[string]any{"listChanged": false},
				"resources": map[string]any{"listChanged": false},
				"prompts":   map[string]any{"listChanged": false},
			},
		}
	case "tools/list":
		a.mu.Lock()
		out := make([]ToolDescriptor, 0, len(a.tools))
		for _, t := range a.tools {
			out = append(out, t.desc)
		}
		a.mu.Unlock()
		// stable order
		sortTools(out)
		resp.Result = map[string]any{"tools": out}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: err.Error()}
			return resp
		}
		a.mu.Lock()
		t, ok := a.tools[p.Name]
		a.mu.Unlock()
		if !ok {
			resp.Error = &rpcError{Code: -32601, Message: fmt.Sprintf("tool %q not found", p.Name)}
			return resp
		}
		out, err := t.handle(ctx, p.Arguments)
		if err != nil {
			// MCP convention: tool errors land in result.isError + content,
			// not in protocol-level error.
			resp.Result = map[string]any{
				"isError": true,
				"content": []map[string]any{{"type": "text", "text": err.Error()}},
			}
			return resp
		}
		bs, _ := json.Marshal(out)
		resp.Result = map[string]any{
			"isError": false,
			"content": []map[string]any{{"type": "text", "text": string(bs)}},
		}
	case "resources/list":
		a.mu.Lock()
		out := make([]ResourceDescriptor, 0, len(a.res)+2)
		for _, r := range a.res {
			out = append(out, r.desc)
		}
		if a.entityResourcePrefix != "" {
			out = append(out, a.entityResourceDesc)
		}
		if a.frameworkResourcePrefix != "" {
			out = append(out, a.frameworkResourceDesc)
		}
		a.mu.Unlock()
		resp.Result = map[string]any{"resources": out}
	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: err.Error()}
			return resp
		}
		// Dynamic gh://framework/<extractor>/<entity> first (P2.T41).
		// Tail format: "<extractor>/<urlencoded Kind:QualifiedName>".
		if a.frameworkResourcePrefix != "" && strings.HasPrefix(p.URI, a.frameworkResourcePrefix) {
			out, ferr := a.handleFrameworkResource(ctx, p.URI)
			if ferr != nil {
				resp.Error = &rpcError{Code: -32602, Message: ferr.Error()}
				return resp
			}
			bs, _ := json.Marshal(out)
			resp.Result = map[string]any{
				"contents": []map[string]any{{
					"uri":      p.URI,
					"mimeType": a.frameworkResourceDesc.MimeType,
					"text":     string(bs),
				}},
			}
			return resp
		}
		// Dynamic gh://entity/code.core/<kind>/<id> first — exact match
		// against the templated descriptor would never hit, so prefix
		// matching is the dispatch.
		if a.entityResourcePrefix != "" && strings.HasPrefix(p.URI, a.entityResourcePrefix) {
			tail := strings.TrimPrefix(p.URI, a.entityResourcePrefix)
			parts := strings.SplitN(tail, "/", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				resp.Error = &rpcError{Code: -32602, Message: fmt.Sprintf("malformed entity URI %q (want gh://entity/code.core/<kind>/<id>)", p.URI)}
				return resp
			}
			svc, err := a.service()
			if err != nil {
				resp.Error = &rpcError{Code: -32603, Message: err.Error()}
				return resp
			}
			out, err := svc.EntityProvenance(ctx, jsonrpc.EntityProvenanceParams{EntityID: parts[1]})
			if err != nil {
				resp.Error = &rpcError{Code: -32603, Message: err.Error()}
				return resp
			}
			bs, _ := json.Marshal(out)
			resp.Result = map[string]any{
				"contents": []map[string]any{{
					"uri":      p.URI,
					"mimeType": a.entityResourceDesc.MimeType,
					"text":     string(bs),
				}},
			}
			return resp
		}
		a.mu.Lock()
		r, ok := a.res[p.URI]
		a.mu.Unlock()
		if !ok {
			resp.Error = &rpcError{Code: -32602, Message: fmt.Sprintf("resource %q not found", p.URI)}
			return resp
		}
		out, err := r.handle(ctx)
		if err != nil {
			resp.Error = &rpcError{Code: -32603, Message: err.Error()}
			return resp
		}
		bs, _ := json.Marshal(out)
		resp.Result = map[string]any{
			"contents": []map[string]any{{
				"uri":      p.URI,
				"mimeType": r.desc.MimeType,
				"text":     string(bs),
			}},
		}
	case "prompts/list":
		// One example prompt — demonstrates the surface.
		resp.Result = map[string]any{
			"prompts": []map[string]any{{
				"name":        "diff_review",
				"description": "Walk an agent through reviewing a unified diff against the workspace flows.",
				"arguments": []map[string]any{
					{"name": "diff", "description": "unified diff", "required": true},
				},
			}},
		}
	case "prompts/get":
		resp.Result = map[string]any{
			"messages": []map[string]any{{
				"role": "user",
				"content": map[string]any{
					"type": "text",
					"text": "Review this diff against the workspace's flow declarations and report unreviewed flow touches.",
				},
			}},
		}
	case "notifications/initialized":
		// no reply for notifications; the Serve loop handles that.
		return resp
	default:
		if !strings.HasPrefix(req.Method, "notifications/") {
			resp.Error = &rpcError{Code: -32601, Message: fmt.Sprintf("method %q not supported", req.Method)}
		}
	}
	return resp
}

// Errors used by callers in tests.
var (
	// ErrToolNotFound is returned when tools/call references an unknown tool.
	ErrToolNotFound = errors.New("tool not found")
)

// FrameworkResourcePayload is the JSON shape returned by
// gh://framework/<extractor>/<entity> resources/read (P2.T41). Entity
// is the canonical EntityProvenanceResult.View for the resolved
// framework entity; Extractor echoes the URI segment for client
// correlation; BoundFlows enumerates flows whose scope covers the
// entity (populated via ImpactedFlows on a selector pointing at this
// entity's qualified name); Findings is currently the active findings
// surfaced by re-running validate.diff on an empty diff — i.e. the
// pipeline's "what's already wrong with this entity" view. The empty
// list is the correct steady-state shape.
type FrameworkResourcePayload struct {
	Extractor  string          `json:"extractor"`
	Kind       string          `json:"kind"`
	Entity     any             `json:"entity"`
	BoundFlows []FlowSummary   `json:"bound_flows"`
	Findings   []any           `json:"findings"`
}

// FlowSummary is the per-flow projection surfaced in
// FrameworkResourcePayload.BoundFlows. Mirrors jsonrpc.FlowImpactRef
// minus the touched-entity counter (always 1 for this entity).
type FlowSummary struct {
	Name          string `json:"name"`
	ScopeSelector string `json:"scope_selector"`
	Description   string `json:"description,omitempty"`
}

// handleFrameworkResource parses a gh://framework/<extractor>/<entity>
// URI, resolves the entity via Consumer.EntityProvenance, validates
// the extractor + kind, and assembles the canonical payload. The
// extractor name is matched against the EntityView's provenance
// ProducedBy field (so "routes.go.chi" matches an entity whose live
// provenance was produced by the routes.go.chi extractor). On
// mismatch the dispatcher still returns the entity payload but
// surfaces the mismatch via a synthetic finding so the agent can
// react.
func (a *Adapter) handleFrameworkResource(ctx context.Context, uri string) (FrameworkResourcePayload, error) {
	tail := strings.TrimPrefix(uri, a.frameworkResourcePrefix)
	// Split into <extractor>/<rest>; the entity segment may contain
	// URL-encoded slashes/colons so we only split once.
	slash := strings.Index(tail, "/")
	if slash <= 0 || slash == len(tail)-1 {
		return FrameworkResourcePayload{}, fmt.Errorf("malformed framework URI %q (want gh://framework/<extractor>/<Kind:QualifiedName>)", uri)
	}
	extractor := tail[:slash]
	rawEntity := tail[slash+1:]
	decoded, err := url.QueryUnescape(rawEntity)
	if err != nil {
		return FrameworkResourcePayload{}, fmt.Errorf("framework URI %q: entity segment is not URL-decodable: %w", uri, err)
	}
	colon := strings.Index(decoded, ":")
	if colon <= 0 || colon == len(decoded)-1 {
		return FrameworkResourcePayload{}, fmt.Errorf("framework URI %q: entity segment must be Kind:QualifiedName", uri)
	}
	kind := decoded[:colon]
	qn := decoded[colon+1:]

	svc, err := a.service()
	if err != nil {
		return FrameworkResourcePayload{}, err
	}
	view, err := svc.EntityProvenance(ctx, jsonrpc.EntityProvenanceParams{QualifiedName: qn})
	if err != nil {
		return FrameworkResourcePayload{}, fmt.Errorf("lookup framework entity %s:%s: %w", kind, qn, err)
	}
	// Best-effort: enumerate flows that resolve this entity. Use the
	// same impacted_flows codepath via a synthetic touched-id seed —
	// but we don't have a public diff route. Instead: list all flows
	// and resolve each against the workspace; mark those whose scope
	// resolution includes this entity. The fast path is FlowsList +
	// SelectorsTest, which both already lower through the kernel.
	flows, ferr := svc.FlowsList(ctx)
	bound := []FlowSummary{}
	if ferr == nil {
		for _, f := range flows.Flows {
			if f.Scope == "" {
				continue
			}
			env, serr := svc.SelectorsTest(ctx, jsonrpc.SelectorsTestParams{Name: f.Scope})
			if serr != nil || env == nil {
				continue
			}
			for _, m := range env.Matches {
				if m.QualifiedName == qn || m.EntityID == view.View.Entity.ID {
					bound = append(bound, FlowSummary{
						Name:          f.Name,
						ScopeSelector: f.Scope,
						Description:   f.Description,
					})
					break
				}
			}
		}
	}
	return FrameworkResourcePayload{
		Extractor:  extractor,
		Kind:       kind,
		Entity:     view.View,
		BoundFlows: bound,
		Findings:   []any{}, // populated by Pass 3 control output stream
	}, nil
}

func sortTools(s []ToolDescriptor) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1].Name > s[j].Name; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
