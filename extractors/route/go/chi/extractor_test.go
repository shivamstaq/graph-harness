package chi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// TestExtractor_RegistersConstructor verifies the package init() landed
// the chi extractor in the package-level registry. This is the
// invariant the orchestrator's all.go relies on to surface the
// extractor without a separate manifest edit.
func TestExtractor_RegistersConstructor(t *testing.T) {
	ctor, desc, ok := code_framework.Lookup(ExtractorName)
	if !ok {
		t.Fatalf("Lookup(%q): not registered", ExtractorName)
	}
	if ctor == nil {
		t.Fatalf("Lookup(%q): nil ctor", ExtractorName)
	}
	if desc.Family != "routes" {
		t.Errorf("Family: got %q, want %q", desc.Family, "routes")
	}
	if len(desc.Outputs) == 0 {
		t.Errorf("Outputs(): empty")
	}
}

// TestExtractor_OnEvent_DetectsAllRoutePatterns runs the extractor over
// the fixture and asserts (method, path) pairs match the expected set.
// Each pattern in the fixture exercises one of the chi router methods
// listed in detectRouteCall's switch.
func TestExtractor_OnEvent_DetectsAllRoutePatterns(t *testing.T) {
	src := mustReadFixture(t, "routes.go.txt")

	tmp := t.TempDir()
	abs := filepath.Join(tmp, "routes.go")
	if err := os.WriteFile(abs, src, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ext, err := New(code_framework.Deps{Workspace: tmp, Logf: t.Logf})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	payload, _ := json.Marshal(map[string]any{"path": "routes.go", "language": "go"})
	ev := kernel.Event{Layer: "code.core", Kind: "FileChanged", Payload: payload}
	emitted, err := ext.OnEvent(context.Background(), ev)
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}

	want := []pair{
		{"GET", "/v1/orders/{id}"},
		{"POST", "/v1/orders"},
		{"PUT", "/v1/orders/{id}"},
		{"PATCH", "/v1/orders/{id}"},
		{"DELETE", "/v1/orders/{id}"},
		{"ANY", "/v1/health"},
		{"CONNECT", "/v1/tunnel"},
		{"GET", "/v1/anonymous"},
	}

	got := collectRoutes(t, emitted)
	if len(got) != len(want) {
		t.Fatalf("route count: got %d, want %d; got=%v", len(got), len(want), got)
	}
	sortPairs := func(p []pair) {
		sort.Slice(p, func(i, j int) bool {
			if p[i].method != p[j].method {
				return p[i].method < p[j].method
			}
			return p[i].pattern < p[j].pattern
		})
	}
	sortPairs(want)
	sortPairs(got)
	for i := range want {
		if want[i] != got[i] {
			t.Errorf("route[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestExtractor_OnEvent_HandlerAnchorKind verifies named handlers
// produce a qualified_name anchor; anonymous closures fall back to
// path_glob and a lower confidence.
func TestExtractor_OnEvent_HandlerAnchorKind(t *testing.T) {
	src := mustReadFixture(t, "routes.go.txt")
	tmp := t.TempDir()
	abs := filepath.Join(tmp, "routes.go")
	if err := os.WriteFile(abs, src, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ext, _ := New(code_framework.Deps{Workspace: tmp, Logf: t.Logf})
	payload, _ := json.Marshal(map[string]any{"path": "routes.go"})
	emitted, err := ext.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	anyAnon := false
	for _, ev := range emitted {
		if ev.Kind != "RouteAdded" {
			continue
		}
		var r code_framework.Route
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("unmarshal route: %v", err)
		}
		if r.PathPattern == "/v1/anonymous" {
			anyAnon = true
			if r.Provenance.Confidence != 0.70 {
				t.Errorf("anonymous handler confidence: got %v, want 0.70", r.Provenance.Confidence)
			}
			// Anonymous closures must still anchor through SelectorRef
			// (not raw code.core IDs) — see SPEC §2.2.
			if len(r.AnchoredTo.Anchors) == 0 {
				t.Errorf("anonymous route: AnchoredTo.Anchors is empty (must include path_glob)")
			}
			for _, a := range r.AnchoredTo.Anchors {
				if a.Kind == "qualified_name" {
					t.Errorf("anonymous route: leaked qualified_name anchor %q", a.Value)
				}
			}
			continue
		}
		if r.Provenance.Confidence != 0.95 {
			t.Errorf("named handler confidence for %s %s: got %v, want 0.95",
				r.Method, r.PathPattern, r.Provenance.Confidence)
		}
		hasQN := false
		for _, a := range r.AnchoredTo.Anchors {
			if a.Kind == "qualified_name" && a.Value != "" {
				hasQN = true
			}
		}
		if !hasQN {
			t.Errorf("named route %s %s missing qualified_name anchor", r.Method, r.PathPattern)
		}
	}
	if !anyAnon {
		t.Errorf("anonymous-closure route not in emission set")
	}
}

// TestExtractor_OnEvent_DeterministicIDs proves re-running OnEvent over
// the same source produces identical Route IDs (the §6.21
// compare-before-emit invariant).
func TestExtractor_OnEvent_DeterministicIDs(t *testing.T) {
	src := mustReadFixture(t, "routes.go.txt")
	tmp := t.TempDir()
	abs := filepath.Join(tmp, "routes.go")
	if err := os.WriteFile(abs, src, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ext, _ := New(code_framework.Deps{Workspace: tmp, Logf: t.Logf})
	payload, _ := json.Marshal(map[string]any{"path": "routes.go"})
	ev := kernel.Event{Layer: "code.core", Kind: "FileChanged", Payload: payload}

	first, err := ext.OnEvent(context.Background(), ev)
	if err != nil {
		t.Fatalf("OnEvent #1: %v", err)
	}
	second, err := ext.OnEvent(context.Background(), ev)
	if err != nil {
		t.Fatalf("OnEvent #2: %v", err)
	}
	idsOf := func(s []kernel.Event) []string {
		var out []string
		for _, e := range s {
			var p map[string]any
			_ = json.Unmarshal(e.Payload, &p)
			if id, ok := p["id"].(string); ok {
				out = append(out, id)
			}
		}
		sort.Strings(out)
		return out
	}
	a := idsOf(first)
	b := idsOf(second)
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatalf("ids drifted across re-extract:\n  first:  %v\n  second: %v", a, b)
	}
}

// TestExtractor_OnEvent_IgnoresNonGo confirms a FileChanged for a .ts
// file produces no emission. This is the cheap filter the extractor
// applies before the os.ReadFile + parse cost.
func TestExtractor_OnEvent_IgnoresNonGo(t *testing.T) {
	ext, _ := New(code_framework.Deps{Workspace: t.TempDir(), Logf: t.Logf})
	payload, _ := json.Marshal(map[string]any{"path": "app.ts"})
	emitted, err := ext.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	if len(emitted) != 0 {
		t.Errorf("non-Go file produced %d events, want 0", len(emitted))
	}
}

type pair struct{ method, pattern string }

func collectRoutes(t *testing.T, evs []kernel.Event) []pair {
	t.Helper()
	var out []pair
	for _, ev := range evs {
		if ev.Kind != "RouteAdded" {
			continue
		}
		var r code_framework.Route
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("unmarshal route: %v", err)
		}
		out = append(out, pair{r.Method, r.PathPattern})
	}
	return out
}

func mustReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return b
}
