package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/dsl"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/semantic_overlay"
)

// newTestService stands up a minimal in-process jsonrpc.Service backed
// by a temp event log + in-memory SQLite code.core store. Returns the
// service (as a Consumer) and the store so tests can seed entities.
// Queue is nil — ImpactedFlows does not exercise the review path.
func newTestService(t *testing.T) (jsonrpc.Consumer, *code_core.Store, *semantic_overlay.Overlay) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.db")
	log, err := facts.OpenEventLog(logPath)
	if err != nil {
		t.Fatalf("OpenEventLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := code_core.NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	overlay := semantic_overlay.NewOverlay()
	ws := &daemon.Workspace{
		Root:       dir,
		StateDir:   filepath.Join(dir, ".graph-harness"),
		EventLog:   logPath,
		SocketPath: filepath.Join(dir, "test.sock"),
		ID:         "test",
	}
	svc := jsonrpc.NewService(ws, log, store, nil, kernel.NewRegistry(), overlay)
	return svc, store, overlay
}

// seedPublisherWithTouch is the MCP-side analogue of
// change_process.seedProducerWithTouch — it co-binds a touched Function
// (qualified_name suffix matches the diff's added decl) to a framework
// producer via a shared selector so the pipeline's reverse-index
// expansion at Stage 4 picks up the producer too.
func seedPublisherWithTouch(t *testing.T, store *code_core.Store,
	funcName, producerKind, producerQN string) string {
	t.Helper()
	ctx := context.Background()
	funcID := "fn:" + funcName
	if err := store.PutEntity(ctx, code_core.Entity{
		ID: funcID, Kind: code_core.KindFunction, LanguageID: "go",
		QualifiedName: "pkg." + funcName,
	}, 1); err != nil {
		t.Fatalf("PutEntity func: %v", err)
	}
	producerID := producerKind + ":" + producerQN
	if err := store.PutEntity(ctx, code_core.Entity{
		ID: producerID, Kind: code_core.EntityKind(producerKind),
		LanguageID:    "framework",
		QualifiedName: producerQN,
	}, 1); err != nil {
		t.Fatalf("PutEntity producer: %v", err)
	}
	sel := "sel:" + producerID
	if err := store.BindSelector(ctx, funcID, sel, "", "qualified_name", 1); err != nil {
		t.Fatalf("BindSelector func: %v", err)
	}
	if err := store.BindSelector(ctx, producerID, sel, "", "framework_anchor", 1); err != nil {
		t.Fatalf("BindSelector producer: %v", err)
	}
	return producerID
}

func seedDependent(t *testing.T, store *code_core.Store, id, kind, qn string) {
	t.Helper()
	if err := store.PutEntity(context.Background(), code_core.Entity{
		ID: id, Kind: code_core.EntityKind(kind), LanguageID: "framework",
		QualifiedName: qn,
	}, 1); err != nil {
		t.Fatalf("PutEntity dependent %s: %v", id, err)
	}
}

func parseFlow(t *testing.T, src string) *dsl.Flow {
	t.Helper()
	f, err := dsl.ParseString("test.gh", src)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	for _, d := range f.Decls {
		if d.Flow != nil {
			return d.Flow
		}
	}
	t.Fatalf("no flow in %q", src)
	return nil
}

func parseSelector(t *testing.T, src string) *dsl.Selector {
	t.Helper()
	f, err := dsl.ParseString("test.gh", src)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	for _, d := range f.Decls {
		if d.Selector != nil {
			return d.Selector
		}
	}
	t.Fatalf("no selector in %q", src)
	return nil
}

func touchDiff(funcName string) string {
	return "--- a/x.go\n+++ b/x.go\n@@\n+func " + funcName + "() {}\n"
}

// TestImpactedFlowsTool_DiffSurfacesFlowAndEvidence is the headline
// P2.T41 case: a diff that touches an EventPublisher must surface a
// flow whose scope contains the publisher AND must surface evidence
// listing every dependent subscriber. The tool is invoked through the
// MCP adapter's tools/call dispatch — not the Service method directly
// — so the wire shape is exercised end-to-end.
func TestImpactedFlowsTool_DiffSurfacesFlowAndEvidence(t *testing.T) {
	svc, store, overlay := newTestService(t)

	// Seed the framework graph: touched PublishOrder fn co-bound to an
	// EventPublisher whose dependents are two subscribers.
	_ = seedPublisherWithTouch(t, store, "PublishOrder",
		"EventPublisher", "kafka:order.created")
	seedDependent(t, store, "sub:svc-a", "EventSubscriber", "kafka:order.created")
	seedDependent(t, store, "sub:svc-b", "EventSubscriber", "kafka:order.created")

	// Selector whose anchor matches the publisher entity's
	// qualified_name. Flow scope binds to that selector.
	overlay.Selectors["OrderPub"] = parseSelector(t, `selector OrderPub {
		anchor qualified_name "kafka:order.created"
	}`)
	overlay.Flows["OrderCreatedFlow"] = parseFlow(t, `flow OrderCreatedFlow {
		description "publish + consume order.created"
		scope OrderPub
	}`)

	adapter := NewAdapter(svc)

	// Dispatch through tools/call so the JSON-RPC envelope is real.
	args, _ := json.Marshal(map[string]string{"diff": touchDiff("PublishOrder")})
	req := rpcReq{
		JSONRPC: "2.0", ID: 1, Method: "tools/call",
		Params: mustJSON(t, map[string]any{
			"name":      "impacted_flows",
			"arguments": json.RawMessage(args),
		}),
	}
	resp := adapter.dispatch(context.Background(), req)
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	out := decodeToolResult(t, resp.Result)

	if len(out.Flows) != 1 || out.Flows[0].Name != "OrderCreatedFlow" {
		t.Fatalf("flows = %+v, want [OrderCreatedFlow]", out.Flows)
	}
	if out.Flows[0].ScopeSelector != "OrderPub" {
		t.Errorf("flow scope_selector = %q, want OrderPub", out.Flows[0].ScopeSelector)
	}
	if out.Flows[0].TouchedEntityCount < 1 {
		t.Errorf("touched_entity_count = %d, want ≥ 1", out.Flows[0].TouchedEntityCount)
	}

	// Evidence: exactly one producer (the EventPublisher) with both
	// subscribers listed as dependents.
	if len(out.Evidence) != 1 {
		t.Fatalf("evidence len = %d, want 1 (got %+v)", len(out.Evidence), out.Evidence)
	}
	fc := out.Evidence[0]
	if fc.TouchedKind != "EventPublisher" {
		t.Errorf("evidence.touched_kind = %q, want EventPublisher", fc.TouchedKind)
	}
	if fc.TouchedSubject.QualifiedName != "kafka:order.created" {
		t.Errorf("evidence.touched_subject.qn = %q, want kafka:order.created",
			fc.TouchedSubject.QualifiedName)
	}
	wantSubs := map[string]bool{"sub:svc-a": true, "sub:svc-b": true}
	got := map[string]bool{}
	for _, d := range fc.Dependents {
		if d.Kind == "EventSubscriber" {
			got[d.ID] = true
		}
	}
	for id := range wantSubs {
		if !got[id] {
			t.Errorf("evidence.dependents missing subscriber %q (got=%+v)", id, fc.Dependents)
		}
	}
}

// TestImpactedFlowsTool_RejectsBothOrNeither enforces the exactly-one
// invariant. The tool must reject `{}` and `{diff,selector}` with a
// clear error rather than silently picking one.
func TestImpactedFlowsTool_RejectsBothOrNeither(t *testing.T) {
	svc, _, _ := newTestService(t)
	adapter := NewAdapter(svc)

	for name, body := range map[string]string{
		"neither": `{}`,
		"both":    `{"diff":"--- a/x\n+++ b/x\n","selector":"X"}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := rpcReq{
				JSONRPC: "2.0", ID: 1, Method: "tools/call",
				Params: mustJSON(t, map[string]any{
					"name":      "impacted_flows",
					"arguments": json.RawMessage(body),
				}),
			}
			resp := adapter.dispatch(context.Background(), req)
			// Tool-level errors land in result.isError per MCP convention.
			m, ok := resp.Result.(map[string]any)
			if !ok {
				t.Fatalf("result not a map: %T", resp.Result)
			}
			if isErr, _ := m["isError"].(bool); !isErr {
				t.Fatalf("expected isError=true, got %+v", m)
			}
		})
	}
}

// TestFrameworkResource_RoundTripsEntityPayload registers a touched
// EventPublisher, then reads gh://framework/<extractor>/<entity> and
// asserts the embedded EntityView JSON matches the stored entity's
// qualified_name + kind. The extractor segment round-trips literally.
func TestFrameworkResource_RoundTripsEntityPayload(t *testing.T) {
	svc, store, overlay := newTestService(t)
	_ = seedPublisherWithTouch(t, store, "PublishOrder",
		"EventPublisher", "kafka:order.created")
	// Bind a flow so bound_flows is non-trivial.
	overlay.Selectors["OrderPub"] = parseSelector(t, `selector OrderPub {
		anchor qualified_name "kafka:order.created"
	}`)
	overlay.Flows["OrderFlow"] = parseFlow(t, `flow OrderFlow {
		scope OrderPub
	}`)

	adapter := NewAdapter(svc)

	// URI: gh://framework/events.go.kafka/EventPublisher:kafka%3Aorder.created
	uri := "gh://framework/events.go.kafka/" +
		url.QueryEscape("EventPublisher:kafka:order.created")
	req := rpcReq{
		JSONRPC: "2.0", ID: 1, Method: "resources/read",
		Params: mustJSON(t, map[string]string{"uri": uri}),
	}
	resp := adapter.dispatch(context.Background(), req)
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result not a map: %T", resp.Result)
	}
	contents, ok := m["contents"].([]map[string]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("contents shape unexpected: %+v", m["contents"])
	}
	payload := FrameworkResourcePayload{}
	if err := json.Unmarshal([]byte(contents[0]["text"].(string)), &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if payload.Extractor != "events.go.kafka" {
		t.Errorf("extractor = %q, want events.go.kafka", payload.Extractor)
	}
	if payload.Kind != "EventPublisher" {
		t.Errorf("kind = %q, want EventPublisher", payload.Kind)
	}
	// Entity is marshalled as an EntityView with nested entity.
	rawEntity, _ := json.Marshal(payload.Entity)
	if !strings.Contains(string(rawEntity), "kafka:order.created") {
		t.Errorf("entity payload missing qualified_name: %s", rawEntity)
	}
	if !strings.Contains(string(rawEntity), "EventPublisher") {
		t.Errorf("entity payload missing kind: %s", rawEntity)
	}
	// Bound flows should contain OrderFlow (it scopes the publisher).
	hit := false
	for _, f := range payload.BoundFlows {
		if f.Name == "OrderFlow" {
			hit = true
		}
	}
	if !hit {
		t.Errorf("bound_flows missing OrderFlow: %+v", payload.BoundFlows)
	}
}

// TestFrameworkResource_RejectsMalformedURI is the negative branch:
// missing extractor segment, missing entity segment, missing colon.
func TestFrameworkResource_RejectsMalformedURI(t *testing.T) {
	svc, _, _ := newTestService(t)
	adapter := NewAdapter(svc)
	bads := []string{
		"gh://framework/",
		"gh://framework/extractor-only",
		"gh://framework/x/no-colon-here",
	}
	for _, uri := range bads {
		req := rpcReq{
			JSONRPC: "2.0", ID: 1, Method: "resources/read",
			Params: mustJSON(t, map[string]string{"uri": uri}),
		}
		resp := adapter.dispatch(context.Background(), req)
		if resp.Error == nil {
			t.Errorf("expected error for %q, got result=%+v", uri, resp.Result)
		}
	}
}

// TestImpactedFlowsTool_ListedInToolsList confirms the descriptor
// surfaces via tools/list so MCP-side agents can discover the tool.
func TestImpactedFlowsTool_ListedInToolsList(t *testing.T) {
	svc, _, _ := newTestService(t)
	adapter := NewAdapter(svc)
	req := rpcReq{JSONRPC: "2.0", ID: 1, Method: "tools/list"}
	resp := adapter.dispatch(context.Background(), req)
	if resp.Error != nil {
		t.Fatalf("tools/list error: %+v", resp.Error)
	}
	m := resp.Result.(map[string]any)
	tools := m["tools"].([]ToolDescriptor)
	found := false
	for _, td := range tools {
		if td.Name == "impacted_flows" {
			found = true
		}
	}
	if !found {
		t.Errorf("impacted_flows not in tools/list: %+v", tools)
	}
}

// TestFrameworkResource_ListedInResourcesList confirms the templated
// resource descriptor appears in resources/list.
func TestFrameworkResource_ListedInResourcesList(t *testing.T) {
	svc, _, _ := newTestService(t)
	adapter := NewAdapter(svc)
	req := rpcReq{JSONRPC: "2.0", ID: 1, Method: "resources/list"}
	resp := adapter.dispatch(context.Background(), req)
	m := resp.Result.(map[string]any)
	resources := m["resources"].([]ResourceDescriptor)
	found := false
	for _, r := range resources {
		if strings.HasPrefix(r.URI, "gh://framework/") {
			found = true
		}
	}
	if !found {
		t.Errorf("gh://framework/ descriptor missing: %+v", resources)
	}
}

// --- helpers ---------------------------------------------------------------

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	bs, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bs
}

// decodeToolResult extracts the inner JSON from an MCP tools/call
// envelope (result.content[0].text). The adapter always wraps the
// tool's return value in that shape (see adapter.dispatch).
func decodeToolResult(t *testing.T, raw any) jsonrpc.ImpactedFlowsResult {
	t.Helper()
	m, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("result not a map: %T", raw)
	}
	if isErr, _ := m["isError"].(bool); isErr {
		t.Fatalf("tool returned isError=true: %+v", m)
	}
	content, ok := m["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content shape unexpected: %+v", m["content"])
	}
	var out jsonrpc.ImpactedFlowsResult
	if err := json.Unmarshal([]byte(content[0]["text"].(string)), &out); err != nil {
		t.Fatalf("unmarshal tool payload: %v", err)
	}
	return out
}
