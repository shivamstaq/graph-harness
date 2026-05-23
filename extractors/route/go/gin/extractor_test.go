package gin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

func TestExtractor_RegistersConstructor(t *testing.T) {
	_, desc, ok := code_framework.Lookup(ExtractorName)
	if !ok {
		t.Fatalf("Lookup(%q): not registered", ExtractorName)
	}
	if desc.Family != "routes" {
		t.Errorf("Family: got %q, want %q", desc.Family, "routes")
	}
}

func TestExtractor_OnEvent_DetectsAllVerbs(t *testing.T) {
	src := readFixture(t, "routes.go.txt")
	tmp := t.TempDir()
	abs := filepath.Join(tmp, "routes.go")
	if err := os.WriteFile(abs, src, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ext, err := New(code_framework.Deps{Workspace: tmp, Logf: t.Logf})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{"path": "routes.go"})
	emitted, err := ext.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}

	type pair struct{ method, pattern string }
	want := []pair{
		{"GET", "/v1/orders/:id"},
		{"POST", "/v1/orders"},
		{"PUT", "/v1/orders/:id"},
		{"PATCH", "/v1/orders/:id"},
		{"DELETE", "/v1/orders/:id"},
		{"HEAD", "/v1/health"},
		{"OPTIONS", "/v1/health"},
		{"ANY", "/v1/wild"},
		{"PROPFIND", "/v1/dav"},
		{"GET", "/v1/anonymous"},
		{"GET", "/v1/with-mw"},
		{"GET", "/orders"}, // api.GET("/orders", ...)
	}
	var got []pair
	for _, ev := range emitted {
		if ev.Kind != "RouteAdded" {
			continue
		}
		var r code_framework.Route
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		got = append(got, pair{r.Method, r.PathPattern})
	}
	sortBy := func(p []pair) {
		sort.Slice(p, func(i, j int) bool {
			if p[i].method != p[j].method {
				return p[i].method < p[j].method
			}
			return p[i].pattern < p[j].pattern
		})
	}
	sortBy(want)
	sortBy(got)
	if len(got) != len(want) {
		t.Fatalf("route count: got %d, want %d; got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("route[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestExtractor_OnEvent_CapturesMiddleware confirms middleware named
// args are surfaced on the Route entity.
func TestExtractor_OnEvent_CapturesMiddleware(t *testing.T) {
	src := readFixture(t, "routes.go.txt")
	tmp := t.TempDir()
	abs := filepath.Join(tmp, "routes.go")
	if err := os.WriteFile(abs, src, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ext, _ := New(code_framework.Deps{Workspace: tmp, Logf: t.Logf})
	payload, _ := json.Marshal(map[string]any{"path": "routes.go"})
	emitted, _ := ext.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	})
	for _, ev := range emitted {
		if ev.Kind != "RouteAdded" {
			continue
		}
		var r code_framework.Route
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if r.PathPattern == "/v1/with-mw" {
			if len(r.Middleware) != 1 || r.Middleware[0] != "AuthMiddleware" {
				t.Errorf("middleware: got %v, want [AuthMiddleware]", r.Middleware)
			}
			return
		}
	}
	t.Errorf("did not find /v1/with-mw route in emission")
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return b
}
