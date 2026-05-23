package express

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
	if len(desc.Frameworks) != 1 || desc.Frameworks[0] != "express" {
		t.Errorf("desc.Frameworks: got %v, want [express]", desc.Frameworks)
	}
	if len(desc.Languages) != 1 || desc.Languages[0] != "typescript" {
		t.Errorf("desc.Languages: got %v, want [typescript]", desc.Languages)
	}
	if len(desc.Inputs) != 1 || desc.Inputs[0] != code_framework.InputCoreFileChanged {
		t.Errorf("desc.Inputs: got %v, want [code.core.FileChanged]", desc.Inputs)
	}
	wantOut := map[code_framework.EntityKind]bool{
		code_framework.KindRoute:   true,
		code_framework.KindHandler: true,
	}
	if len(desc.Outputs) != len(wantOut) {
		t.Errorf("desc.Outputs len: got %d, want %d", len(desc.Outputs), len(wantOut))
	}
	for _, o := range desc.Outputs {
		if !wantOut[o] {
			t.Errorf("desc.Outputs has unexpected %q", o)
		}
	}
}

// TestExtractor_routes drives the extractor over the fixture file and
// asserts the expected (method, pathPattern) set is emitted.
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
		{Method: "DELETE", Path: "/users/:id"},
		{Method: "GET", Path: "/admin/metrics"},
		{Method: "GET", Path: "/health"},
		{Method: "GET", Path: "/profile"},
		{Method: "GET", Path: "/users"},
		{Method: "GET", Path: "/v2/widgets"},
		{Method: "PATCH", Path: "/users/:id"},
		{Method: "POST", Path: "/users"},
		{Method: "PUT", Path: "/users/:id"},
	}
	assertRouteSet(t, got, want)
}

// TestExtractor_ignoresNonTS ensures non-TypeScript paths produce no
// events.
func TestExtractor_ignoresNonTS(t *testing.T) {
	wd := mustWorkspace(t)
	ext, err := newExtractor(code_framework.Deps{Workspace: wd})
	if err != nil {
		t.Fatalf("newExtractor: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{"path": "README.md"})
	events, err := ext.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload, Seq: 1,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("non-TS path produced %d events, want 0", len(events))
	}
}

// TestExtractor_idempotentIDs proves that running the extractor twice
// over the same fixture produces identical content ids per route —
// the compare-before-emit precondition.
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

// TestExtractor_handlerAnchorPreference verifies that an
// identifier-handler route emits a SelectorRef whose first anchor is
// qualified_name, while an inline-arrow route uses path_glob only.
func TestExtractor_handlerAnchorPreference(t *testing.T) {
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
	routes := unmarshalRoutes(t, events)
	for _, r := range routes {
		switch {
		case r.Method == "GET" && r.PathPattern == "/users":
			// identifier handler `listUsers` → qualified_name first
			if len(r.AnchoredTo.Anchors) == 0 || r.AnchoredTo.Anchors[0].Kind != "qualified_name" {
				t.Errorf("GET /users: want first anchor qualified_name, got %#v", r.AnchoredTo.Anchors)
			}
		case r.Method == "GET" && r.PathPattern == "/health":
			// inline arrow → no qualified_name, only path_glob (+
			// body_hash for inline span)
			for _, a := range r.AnchoredTo.Anchors {
				if a.Kind == "qualified_name" {
					t.Errorf("GET /health: inline-arrow route should NOT carry qualified_name anchor")
				}
			}
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

func unmarshalRoutes(t *testing.T, events []kernel.Event) []code_framework.Route {
	t.Helper()
	var out []code_framework.Route
	for _, ev := range events {
		if ev.Kind != "RouteAdded" {
			continue
		}
		var r code_framework.Route
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("unmarshal route: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func assertRouteSet(t *testing.T, got, want []routeKey) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("route count: got %d, want %d\n  got=%v\n  want=%v", len(got), len(want), got, want)
	}
	gotSet := make(map[routeKey]int)
	for _, g := range got {
		gotSet[g]++
	}
	for _, w := range want {
		if gotSet[w] == 0 {
			t.Errorf("missing route %v", w)
		}
	}
	for g, n := range gotSet {
		if !containsRoute(want, g) {
			t.Errorf("unexpected route %v (x%d)", g, n)
		}
	}
}

func containsRoute(want []routeKey, k routeKey) bool {
	for _, w := range want {
		if w == k {
			return true
		}
	}
	return false
}

// mustWorkspace returns the directory containing the testdata folder.
// The fixture path embedded in the FileChanged payload is
// "testdata/app.ts", joined with the workspace at parse time.
func mustWorkspace(t *testing.T) string {
	t.Helper()
	wd, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	return wd
}
