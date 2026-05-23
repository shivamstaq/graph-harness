package fastapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

func TestExtractor_FastAPI(t *testing.T) {
	ext, err := New(code_framework.Deps{Workspace: "."})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	payload, _ := json.Marshal(map[string]string{"path": "testdata/app.py", "language": "python"})
	events, err := ext.OnEvent(context.Background(), kernel.Event{
		Layer:   "code.core",
		Kind:    "FileChanged",
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}

	// We expect 6 routes (health, login, list_orders, update_order,
	// delete_order, patch_order) → 12 events (Route + Handler each).
	const wantRoutes = 6
	if got := len(events); got != wantRoutes*2 {
		t.Fatalf("events: got %d, want %d", got, wantRoutes*2)
	}

	routes := decodeRoutes(t, events)
	if len(routes) != wantRoutes {
		t.Fatalf("routes: got %d, want %d", len(routes), wantRoutes)
	}

	type want struct {
		method  string
		path    string
		handler string
	}
	wantSet := map[want]bool{
		{"GET", "/health", "testdata.app.health"}:                          false,
		{"POST", "/login", "testdata.app.login"}:                           false,
		{"GET", "/api/v1/orders", "testdata.app.list_orders"}:              false,
		{"PUT", "/api/v1/orders/{order_id}", "testdata.app.update_order"}:  false,
		{"DELETE", "/api/v1/orders/{order_id}", "testdata.app.delete_order"}: false,
		{"PATCH", "/api/v1/orders/{order_id}", "testdata.app.patch_order"}: false,
	}
	for _, r := range routes {
		if r.Framework != "fastapi" {
			t.Errorf("route framework: got %q, want %q", r.Framework, "fastapi")
		}
		key := want{r.Method, r.PathPattern, ""}
		// Find the matching wanted record by (method, path); also
		// verify the handler qn carries via the anchor.
		for w := range wantSet {
			if w.method == key.method && w.path == key.path {
				if !routeHandlerMatches(r, w.handler) {
					t.Errorf("handler mismatch for %s %s: got %v, want %q",
						r.Method, r.PathPattern, r.AnchoredTo, w.handler)
				}
				wantSet[w] = true
			}
		}
		if r.Provenance.Confidence < 0.9 {
			t.Errorf("confidence too low for literal-decorator route %s %s: %.2f",
				r.Method, r.PathPattern, r.Provenance.Confidence)
		}
	}
	for w, seen := range wantSet {
		if !seen {
			t.Errorf("missing expected route: %+v", w)
		}
	}
}

func TestExtractor_FastAPI_NonPython_NoOp(t *testing.T) {
	ext, _ := New(code_framework.Deps{Workspace: "."})
	payload, _ := json.Marshal(map[string]string{"path": "testdata/app.go"})
	events, err := ext.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("non-python OnEvent: got %d events, want 0", len(events))
	}
}

func TestExtractor_FastAPI_MissingFile_NoOp(t *testing.T) {
	ext, _ := New(code_framework.Deps{Workspace: "."})
	payload, _ := json.Marshal(map[string]string{"path": "testdata/does-not-exist.py"})
	events, err := ext.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("missing-file OnEvent: got %d events, want 0", len(events))
	}
}

func TestExtractor_FastAPI_Descriptor(t *testing.T) {
	ctor, desc, ok := code_framework.Lookup(Name)
	if !ok {
		t.Fatalf("Lookup(%q): not registered", Name)
	}
	if ctor == nil {
		t.Fatal("ctor must be non-nil")
	}
	if desc.Family != "routes" {
		t.Errorf("family: got %q, want %q", desc.Family, "routes")
	}
	if len(desc.Outputs) != 2 {
		t.Errorf("outputs: got %d, want 2", len(desc.Outputs))
	}
}

// decodeRoutes pulls out the Route entities from a mixed event slice.
func decodeRoutes(t *testing.T, events []kernel.Event) []code_framework.Route {
	t.Helper()
	var out []code_framework.Route
	for _, ev := range events {
		if ev.Kind != "RouteAdded" {
			continue
		}
		var r code_framework.Route
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("decode Route: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// routeHandlerMatches checks the Route's AnchoredTo carries a
// qualified_name anchor with the expected value.
func routeHandlerMatches(r code_framework.Route, wantQN string) bool {
	for _, a := range r.AnchoredTo.Anchors {
		if a.Kind == "qualified_name" && a.Value == wantQN {
			return true
		}
	}
	return false
}
