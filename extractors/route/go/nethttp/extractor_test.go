package nethttp

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

func TestExtractor_OnEvent_DetectsPreAndPost122Patterns(t *testing.T) {
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
		{"ANY", "/v1/health"},
		{"ANY", "/v1/static"},
		{"GET", "/v1/orders/{id}"},
		{"POST", "/v1/orders"},
		{"PUT", "/v1/orders/{id}"},
		{"DELETE", "/v1/orders/{id}"},
		{"ANY", "/v1/wild"},
		{"GET", "/v1/anonymous"},
		{"ANY", "FOO /v1/bogus"}, // verb-not-in-list → ANY, raw pattern kept
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

// TestSplitMethodPattern exercises the Go 1.22+ pattern-splitting logic
// in isolation. Important because the same logic underpins the v1
// extractor's reliability on non-Go-1.22 codebases.
func TestSplitMethodPattern(t *testing.T) {
	cases := []struct {
		in           string
		wantMethod   string
		wantPattern  string
	}{
		{"GET /api/orders", "GET", "/api/orders"},
		{"POST /api/orders/{id}", "POST", "/api/orders/{id}"},
		{"/api/health", "ANY", "/api/health"},
		{"GET example.com/api", "GET", "example.com/api"},
		{"FOO /bar", "ANY", "FOO /bar"},
		{"", "ANY", ""},
		{"GET", "ANY", "GET"},
	}
	for _, c := range cases {
		m, p := splitMethodPattern(c.in)
		if m != c.wantMethod || p != c.wantPattern {
			t.Errorf("splitMethodPattern(%q): got (%q,%q), want (%q,%q)",
				c.in, m, p, c.wantMethod, c.wantPattern)
		}
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return b
}
