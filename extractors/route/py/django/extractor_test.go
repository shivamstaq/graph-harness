package django

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

func TestExtractor_Django(t *testing.T) {
	ext, err := New(code_framework.Deps{Workspace: "."})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	payload, _ := json.Marshal(map[string]string{"path": "testdata/urls.py", "language": "python"})
	events, err := ext.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}

	// Expected detected routes (method, path, handlerQN substring,
	// minConfidence):
	//  GET                      /health/             testdata.urls.health           literal (require_GET)
	//  POST,PUT                 /things/             testdata.urls.save_thing       literal
	//  ANY                      /orders/             views.orders_index             computed (cross-module ref)
	//  ANY                      /orders/<int:pk>/    views.order_detail             computed
	//  ANY                      /^legacy/(?P<...>...) views.legacy_view             computed
	//  ANY                      /myapp.api.urls      myapp.api.urls                 dynamic (include)
	//  ANY                      /admin/              views.AdminPanel               computed (.as_view)
	type want struct {
		method     string
		pathHas    string // substring match because regex pattern differs across grammars
		handlerHas string
		minConf    float64
	}
	wants := []want{
		{"GET", "/health/", "testdata.urls.health", 0.9},
		{"POST", "/things/", "testdata.urls.save_thing", 0.9},
		{"PUT", "/things/", "testdata.urls.save_thing", 0.9},
		{"ANY", "/orders/", "views.orders_index", 0.8},
		{"ANY", "/orders/<int:pk>/", "views.order_detail", 0.8},
		{"ANY", "legacy", "views.legacy_view", 0.8},
		{"ANY", "/api/", "myapp.api.urls", 0.6},
		{"ANY", "/admin/", "views.AdminPanel", 0.8},
	}

	routes := decodeRoutes(t, events)
	if got, min := len(routes), len(wants); got < min {
		t.Fatalf("routes: got %d, want >= %d\nroutes: %v", got, min, routes)
	}

	matched := make([]bool, len(wants))
	for _, r := range routes {
		hQN := handlerQN(r)
		for i, w := range wants {
			if matched[i] {
				continue
			}
			if w.method != r.Method {
				continue
			}
			if !strings.Contains(r.PathPattern, w.pathHas) {
				continue
			}
			if w.handlerHas != "" && !strings.Contains(hQN, w.handlerHas) {
				continue
			}
			if r.Provenance.Confidence+1e-6 < w.minConf {
				continue
			}
			matched[i] = true
			break
		}
	}
	for i, ok := range matched {
		if !ok {
			t.Errorf("unmatched want[%d]: %+v\nall routes:\n%s", i, wants[i], dump(routes))
			break
		}
	}
}

func TestExtractor_Django_OnlyURLConfFiles(t *testing.T) {
	ext, _ := New(code_framework.Deps{Workspace: "."})
	// Non-urls.py path should produce nothing even if it's Python.
	payload, _ := json.Marshal(map[string]string{"path": "testdata/views.py"})
	events, err := ext.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("non-urls path: got %d events, want 0", len(events))
	}
}

func TestExtractor_Django_Descriptor(t *testing.T) {
	_, desc, ok := code_framework.Lookup(Name)
	if !ok {
		t.Fatalf("Lookup(%q): not registered", Name)
	}
	if desc.Family != "routes" {
		t.Errorf("family: got %q, want %q", desc.Family, "routes")
	}
	if len(desc.Frameworks) == 0 || desc.Frameworks[0] != "django" {
		t.Errorf("frameworks: got %v, want [django]", desc.Frameworks)
	}
}

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
	// Sort for stable diff output.
	sort.Slice(out, func(i, j int) bool {
		if out[i].PathPattern != out[j].PathPattern {
			return out[i].PathPattern < out[j].PathPattern
		}
		return out[i].Method < out[j].Method
	})
	return out
}

func handlerQN(r code_framework.Route) string {
	for _, a := range r.AnchoredTo.Anchors {
		if a.Kind == "qualified_name" {
			return a.Value
		}
	}
	return ""
}

func dump(rs []code_framework.Route) string {
	var b strings.Builder
	for _, r := range rs {
		b.WriteString(r.Method)
		b.WriteString(" ")
		b.WriteString(r.PathPattern)
		b.WriteString(" -> ")
		b.WriteString(handlerQN(r))
		b.WriteString("\n")
	}
	return b.String()
}
