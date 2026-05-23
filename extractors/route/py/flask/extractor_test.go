package flask

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

func TestExtractor_Flask(t *testing.T) {
	ext, err := New(code_framework.Deps{Workspace: "."})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	payload, _ := json.Marshal(map[string]string{"path": "testdata/app.py", "language": "python"})
	events, err := ext.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}

	// Expected routes:
	//  GET    /health         testdata.app.health
	//  POST   /login          testdata.app.login
	//  GET    /orders         testdata.app.orders
	//  POST   /orders         testdata.app.orders
	//  GET    /api/users/<int:user_id>  testdata.app.get_user
	//  PUT    /api/users/<int:user_id>  testdata.app.update_user
	//  PATCH  /api/users/<int:user_id>  testdata.app.update_user
	expect := []rid{
		{"GET", "/health", "testdata.app.health"},
		{"POST", "/login", "testdata.app.login"},
		{"GET", "/orders", "testdata.app.orders"},
		{"POST", "/orders", "testdata.app.orders"},
		{"GET", "/api/users/<int:user_id>", "testdata.app.get_user"},
		{"PUT", "/api/users/<int:user_id>", "testdata.app.update_user"},
		{"PATCH", "/api/users/<int:user_id>", "testdata.app.update_user"},
	}
	got := []rid{}
	for _, ev := range events {
		if ev.Kind != "RouteAdded" {
			continue
		}
		var r code_framework.Route
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if r.Framework != "flask" {
			t.Errorf("framework: got %q, want %q", r.Framework, "flask")
		}
		if r.Provenance.Confidence < 0.9 {
			t.Errorf("confidence: %s %s = %.2f", r.Method, r.PathPattern, r.Provenance.Confidence)
		}
		got = append(got, rid{r.Method, r.PathPattern, handlerQN(r)})
	}
	sortRids(expect)
	sortRids(got)
	if !equalRids(got, expect) {
		t.Fatalf("routes mismatch:\n got=%v\nwant=%v", got, expect)
	}
}

func TestExtractor_Flask_Descriptor(t *testing.T) {
	_, desc, ok := code_framework.Lookup(Name)
	if !ok {
		t.Fatalf("Lookup(%q): not registered", Name)
	}
	if desc.Family != "routes" {
		t.Errorf("family: got %q, want %q", desc.Family, "routes")
	}
	if len(desc.Frameworks) == 0 || desc.Frameworks[0] != "flask" {
		t.Errorf("frameworks: got %v, want [flask]", desc.Frameworks)
	}
}

type rid struct{ method, path, handler string }

func sortRids(xs []rid) {
	sort.Slice(xs, func(i, j int) bool {
		if xs[i].path != xs[j].path {
			return xs[i].path < xs[j].path
		}
		return xs[i].method < xs[j].method
	})
}
func equalRids(a, b []rid) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func handlerQN(r code_framework.Route) string {
	for _, a := range r.AnchoredTo.Anchors {
		if a.Kind == "qualified_name" {
			return a.Value
		}
	}
	return ""
}
