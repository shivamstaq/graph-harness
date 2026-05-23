package jest

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
		if d.Family != "tests" {
			t.Errorf("Family: %q want tests", d.Family)
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
			Layer: "code.core", Kind: "FileChanged", Payload: payload, Seq: 3,
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
				if test.Framework != "jest" {
					t.Errorf("Framework: %q want jest", test.Framework)
				}
				names = append(names, test.Name)
			case "FixtureAdded":
				fixtures++
			}
		}
		sort.Strings(names)
		// Expect 4 tests: OrderService/places an order,
		//                 OrderService/cancel/cancels happy path,
		//                 OrderService/cancel/noop on already-cancelled,
		//                 OrderService/publishes order.created.
		if len(names) != 4 {
			t.Errorf("got %d tests (%v), want 4", len(names), names)
		}
		// Fixtures: beforeEach + afterEach.
		if fixtures != 2 {
			t.Errorf("got %d fixtures, want 2", fixtures)
		}
		// At least one test name contains describe nesting.
		found := false
		for _, n := range names {
			if len(n) > len("OrderService/") && n[:len("OrderService/")] == "OrderService/" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected describe-nested name; got %v", names)
		}
	})
	t.Run("vitest-file-skipped", func(t *testing.T) {
		ws := t.TempDir()
		vt := `import { describe, it } from "vitest";
describe("Foo", () => { it("bar", () => {}) });`
		if err := os.WriteFile(filepath.Join(ws, "v.test.ts"), []byte(vt), 0o644); err != nil {
			t.Fatal(err)
		}
		e, _ := New(code_framework.Deps{Workspace: ws})
		payload, _ := json.Marshal(map[string]any{"path": "v.test.ts"})
		out, err := e.OnEvent(context.Background(), kernel.Event{Layer: "code.core", Kind: "FileChanged", Payload: payload})
		if err != nil {
			t.Fatalf("OnEvent: %v", err)
		}
		if len(out) != 0 {
			t.Errorf("Jest extractor should skip vitest-imported file, got %d events", len(out))
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
