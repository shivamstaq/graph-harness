package common

import (
	"encoding/json"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

func TestJoinPath(t *testing.T) {
	cases := []struct {
		prefix, sub, want string
	}{
		{"", "", "/"},
		{"", "/foo", "/foo"},
		{"/api", "/users", "/api/users"},
		{"/api/", "users", "/api/users"},
		{"/api", "", "/api"},
		{"", "users", "/users"},
	}
	for _, tc := range cases {
		if got := JoinPath(tc.prefix, tc.sub); got != tc.want {
			t.Errorf("JoinPath(%q,%q)=%q, want %q", tc.prefix, tc.sub, got, tc.want)
		}
	}
}

func TestModuleName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"app.py", "app"},
		{"api/urls.py", "api.urls"},
		{"app/__init__.py", "app"},
		{"./app.py", "app"},
	}
	for _, tc := range cases {
		if got := ModuleName(tc.in); got != tc.want {
			t.Errorf("ModuleName(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMethodCanon(t *testing.T) {
	cases := []struct{ in, want string }{
		{"get", "GET"},
		{" 'post' ", "POST"},
		{`"put"`, "PUT"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := MethodCanon(tc.in); got != tc.want {
			t.Errorf("MethodCanon(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestToEvents_RouteHandlerPairing(t *testing.T) {
	routes := []ExtractedRoute{{
		Method:      "GET",
		PathPattern: "/x",
		Framework:   "test",
		HandlerQN:   "mod.fn",
		Confidence:  ConfidenceLiteral,
		SourcePath:  "x.py",
	}}
	events, err := ToEvents("routes.py.test", routes)
	if err != nil {
		t.Fatalf("ToEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events: got %d, want 2", len(events))
	}
	if events[0].Kind != "RouteAdded" {
		t.Errorf("event[0].Kind = %q, want RouteAdded", events[0].Kind)
	}
	if events[1].Kind != "HandlerBound" {
		t.Errorf("event[1].Kind = %q, want HandlerBound", events[1].Kind)
	}
	if events[0].Layer != "code.framework" {
		t.Errorf("event layer: got %q, want code.framework", events[0].Layer)
	}

	var r code_framework.Route
	if err := json.Unmarshal(events[0].Payload, &r); err != nil {
		t.Fatalf("unmarshal Route: %v", err)
	}
	if r.Method != "GET" || r.PathPattern != "/x" || r.Framework != "test" {
		t.Errorf("Route fields: %+v", r)
	}
	if r.AnchoredTo.Anchors[0].Kind != "qualified_name" {
		t.Errorf("anchor kind: got %q, want qualified_name", r.AnchoredTo.Anchors[0].Kind)
	}
	if r.Provenance.ProducedBy != "extractor:framework:routes.py.test" {
		t.Errorf("produced_by: %q", r.Provenance.ProducedBy)
	}
	// Ensure the layer constant matches what the dispatcher expects.
	if string(code_framework.SourceExtractorFramework) != "extractor:framework" {
		t.Errorf("SourceExtractorFramework constant drift")
	}
}

func TestDecodeFileChanged_Empty(t *testing.T) {
	p, err := DecodeFileChanged(kernel.Event{})
	if err != nil {
		t.Fatalf("DecodeFileChanged empty: %v", err)
	}
	if p.Path != "" {
		t.Errorf("Path: got %q, want empty", p.Path)
	}
}

func TestIsPythonPath(t *testing.T) {
	if !IsPythonPath("a/b/c.py") {
		t.Error("expected .py to be Python")
	}
	if IsPythonPath("a/b/c.go") {
		t.Error("expected .go to not be Python")
	}
}
