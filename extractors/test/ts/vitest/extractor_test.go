package vitest

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

func TestExtractorContract(t *testing.T) {
	t.Run("descriptor-shape", func(t *testing.T) {
		d := Descriptor()
		if d.Name != Name {
			t.Fatalf("Name: %q want %q", d.Name, Name)
		}
	})
	t.Run("emit-from-fixture", func(t *testing.T) {
		ws := t.TempDir()
		copyFixture(t, "testdata/sample.test.ts.txt", filepath.Join(ws, "sample.test.ts"))

		e, err := New(code_framework.Deps{Workspace: ws})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		payload, _ := json.Marshal(map[string]any{
			"path":     "sample.test.ts",
			"language": "typescript",
		})
		out, err := e.OnEvent(context.Background(), kernel.Event{
			Layer: "code.core", Kind: "FileChanged", Payload: payload, Seq: 1,
		})
		if err != nil {
			t.Fatalf("OnEvent: %v", err)
		}
		var (
			names    []string
			fixtures int
		)
		for _, ev := range out {
			switch ev.Kind {
			case "TestAdded":
				var test code_framework.Test
				if err := json.Unmarshal(ev.Payload, &test); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if test.Framework != "vitest" {
					t.Errorf("Framework: %q want vitest", test.Framework)
				}
				names = append(names, test.Name)
			case "FixtureAdded":
				fixtures++
			}
		}
		sort.Strings(names)
		// Expect 3 tests + 1 fixture (beforeEach).
		if len(names) != 3 {
			t.Errorf("tests: got %d (%v), want 3", len(names), names)
		}
		if fixtures != 1 {
			t.Errorf("fixtures: got %d, want 1", fixtures)
		}
	})
	t.Run("jest-file-skipped", func(t *testing.T) {
		ws := t.TempDir()
		js := `describe("Foo", () => { it("bar", () => {}) });`
		if err := os.WriteFile(filepath.Join(ws, "j.test.ts"), []byte(js), 0o644); err != nil {
			t.Fatal(err)
		}
		e, _ := New(code_framework.Deps{Workspace: ws})
		payload, _ := json.Marshal(map[string]any{"path": "j.test.ts"})
		out, err := e.OnEvent(context.Background(), kernel.Event{Layer: "code.core", Kind: "FileChanged", Payload: payload})
		if err != nil {
			t.Fatalf("OnEvent: %v", err)
		}
		if len(out) != 0 {
			t.Errorf("vitest extractor should skip file without vitest import, got %d events", len(out))
		}
	})
}

func copyFixture(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture %s: %v", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}
