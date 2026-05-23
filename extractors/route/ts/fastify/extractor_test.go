package fastify

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// TestRegistered asserts the package init() ran and the extractor is
// queryable through the registry by name.
func TestRegistered(t *testing.T) {
	_, desc, ok := code_framework.Lookup(extractorName)
	if !ok {
		t.Fatalf("Lookup(%q): not registered", extractorName)
	}
	if desc.Family != "routes" {
		t.Errorf("desc.Family: got %q, want %q", desc.Family, "routes")
	}
	if len(desc.Frameworks) != 1 || desc.Frameworks[0] != "fastify" {
		t.Errorf("desc.Frameworks: got %v, want [fastify]", desc.Frameworks)
	}
}

// TestExtractor_routes drives the fastify fixture and asserts the
// expected route set. The fixture contains 7 routes:
//   - 4 from `fastify.<method>(...)` shortcuts (GET /health, GET
//     /orders, POST /orders, plus the inner instance.get('/health')
//     from the plugin closure body).
//   - 3 from `.route({...})` (PUT /orders/:id and the
//     ['GET','HEAD'] /orders/:id/snapshot pair).
func TestExtractor_routes(t *testing.T) {
	wd := mustWorkspace(t)
	ext, err := newExtractor(code_framework.Deps{Workspace: wd})
	if err != nil {
		t.Fatalf("newExtractor: %v", err)
	}

	payload, _ := json.Marshal(map[string]any{"path": "testdata/app.ts"})
	events, err := ext.OnEvent(context.Background(), kernel.Event{
		Layer:   "code.core",
		Kind:    "FileChanged",
		Payload: payload,
		Seq:     1,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}

	got := collectRoutes(t, events)
	want := []routeKey{
		{Method: "GET", Path: "/health"},                 // outer fastify.get
		{Method: "GET", Path: "/health"},                 // inner instance.get inside register's plugin function
		{Method: "GET", Path: "/orders"},                 // identifier handler
		{Method: "GET", Path: "/orders/:id/snapshot"},    // .route array methods
		{Method: "HEAD", Path: "/orders/:id/snapshot"},   // .route array methods
		{Method: "POST", Path: "/orders"},                // options-bag handler
		{Method: "PUT", Path: "/orders/:id"},             // .route string method
	}
	assertRouteSet(t, got, want)
}

// TestExtractor_methodArrayEmitsPerMethod proves the array form of
// `method:[...]` expands to one Route per element.
func TestExtractor_methodArrayEmitsPerMethod(t *testing.T) {
	wd := mustWorkspace(t)
	ext, err := newExtractor(code_framework.Deps{Workspace: wd})
	if err != nil {
		t.Fatalf("newExtractor: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{"path": "testdata/app.ts"})
	events, err := ext.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload, Seq: 1,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	snapshots := 0
	for _, ev := range events {
		if ev.Kind != "RouteAdded" {
			continue
		}
		var r code_framework.Route
		_ = json.Unmarshal(ev.Payload, &r)
		if r.PathPattern == "/orders/:id/snapshot" {
			snapshots++
		}
	}
	if snapshots != 2 {
		t.Errorf("/orders/:id/snapshot count: got %d, want 2", snapshots)
	}
}

// TestExtractor_idempotentIDs proves identical re-parses generate
// identical content ids per route.
func TestExtractor_idempotentIDs(t *testing.T) {
	wd := mustWorkspace(t)
	ext, err := newExtractor(code_framework.Deps{Workspace: wd})
	if err != nil {
		t.Fatalf("newExtractor: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{"path": "testdata/app.ts"})
	in := kernel.Event{Layer: "code.core", Kind: "FileChanged", Payload: payload, Seq: 1}
	first, err := ext.OnEvent(context.Background(), in)
	if err != nil {
		t.Fatalf("OnEvent #1: %v", err)
	}
	second, err := ext.OnEvent(context.Background(), in)
	if err != nil {
		t.Fatalf("OnEvent #2: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("event count: first=%d, second=%d", len(first), len(second))
	}
	for i := range first {
		var a, b struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(first[i].Payload, &a)
		_ = json.Unmarshal(second[i].Payload, &b)
		if a.ID == "" || a.ID != b.ID {
			t.Errorf("event %d id mismatch: %q vs %q", i, a.ID, b.ID)
		}
	}
}

// ---- helpers --------------------------------------------------------------

type routeKey struct {
	Method string
	Path   string
}

func collectRoutes(t *testing.T, events []kernel.Event) []routeKey {
	t.Helper()
	var got []routeKey
	for _, ev := range events {
		if ev.Kind != "RouteAdded" {
			continue
		}
		var r code_framework.Route
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("unmarshal route: %v", err)
		}
		got = append(got, routeKey{Method: r.Method, Path: r.PathPattern})
	}
	sort.Slice(got, func(i, j int) bool {
		if got[i].Method != got[j].Method {
			return got[i].Method < got[j].Method
		}
		return got[i].Path < got[j].Path
	})
	return got
}

func assertRouteSet(t *testing.T, got, want []routeKey) {
	t.Helper()
	// Sort want too so equal multisets compare cleanly.
	sort.Slice(want, func(i, j int) bool {
		if want[i].Method != want[j].Method {
			return want[i].Method < want[j].Method
		}
		return want[i].Path < want[j].Path
	})
	if len(got) != len(want) {
		t.Errorf("route count: got %d, want %d\n  got=%v\n  want=%v", len(got), len(want), got, want)
		return
	}
	gotMap := multiset(got)
	wantMap := multiset(want)
	for k, n := range wantMap {
		if gotMap[k] != n {
			t.Errorf("route %v: got count %d, want %d", k, gotMap[k], n)
		}
	}
	for k, n := range gotMap {
		if wantMap[k] != n {
			t.Errorf("unexpected route %v: got count %d, want %d", k, n, wantMap[k])
		}
	}
}

func multiset(rs []routeKey) map[routeKey]int {
	out := make(map[routeKey]int, len(rs))
	for _, r := range rs {
		out[r]++
	}
	return out
}

func mustWorkspace(t *testing.T) string {
	t.Helper()
	wd, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	return wd
}
