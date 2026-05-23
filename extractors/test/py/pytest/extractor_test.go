package pytest

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
		copyFixture(t, "testdata/test_sample.py.txt", filepath.Join(ws, "test_sample.py"))

		e, err := New(code_framework.Deps{Workspace: ws})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		payload, _ := json.Marshal(map[string]any{
			"path":     "test_sample.py",
			"language": "python",
		})
		out, err := e.OnEvent(context.Background(), kernel.Event{
			Layer: "code.core", Kind: "FileChanged", Payload: payload, Seq: 5,
		})
		if err != nil {
			t.Fatalf("OnEvent: %v", err)
		}

		var (
			testNames    []string
			fixtureNames []string
		)
		for _, ev := range out {
			switch ev.Kind {
			case "TestAdded":
				var test code_framework.Test
				if err := json.Unmarshal(ev.Payload, &test); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if test.Framework != "pytest" {
					t.Errorf("Framework: %q want pytest", test.Framework)
				}
				testNames = append(testNames, test.Name)
			case "FixtureAdded":
				var fx code_framework.Fixture
				if err := json.Unmarshal(ev.Payload, &fx); err != nil {
					t.Fatalf("unmarshal fixture: %v", err)
				}
				if len(fx.AnchoredTo.Anchors) == 0 {
					t.Errorf("fixture missing anchor")
					continue
				}
				fixtureNames = append(fixtureNames, fx.AnchoredTo.Anchors[0].Value)
			}
		}
		sort.Strings(testNames)
		sort.Strings(fixtureNames)
		if len(testNames) != 3 {
			t.Errorf("tests: got %d (%v), want 3", len(testNames), testNames)
		}
		if len(fixtureNames) != 2 {
			t.Errorf("fixtures: got %d (%v), want 2", len(fixtureNames), fixtureNames)
		}
	})
	t.Run("non-test-file-ignored", func(t *testing.T) {
		ws := t.TempDir()
		if err := os.WriteFile(filepath.Join(ws, "regular.py"), []byte("def test_foo(): pass\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		e, _ := New(code_framework.Deps{Workspace: ws})
		payload, _ := json.Marshal(map[string]any{"path": "regular.py"})
		out, err := e.OnEvent(context.Background(), kernel.Event{Layer: "code.core", Kind: "FileChanged", Payload: payload})
		if err != nil {
			t.Fatalf("OnEvent: %v", err)
		}
		if len(out) != 0 {
			t.Errorf("regular.py produced %d events; want 0", len(out))
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
