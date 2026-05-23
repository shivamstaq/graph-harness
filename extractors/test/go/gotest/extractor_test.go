package gotest

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

// TestExtractorContract is the per-extractor gate: one fixture, asserts
// the expected Test rows + sub-tests + ContractTest emit shape.
func TestExtractorContract(t *testing.T) {
	t.Run("descriptor-shape", testDescriptorShape)
	t.Run("emit-from-fixture", testEmitFromFixture)
	t.Run("ignores-non-test-files", testIgnoresNonTestFiles)
}

func testDescriptorShape(t *testing.T) {
	d := Descriptor()
	if d.Name != Name {
		t.Fatalf("Descriptor.Name: got %q, want %q", d.Name, Name)
	}
	if d.Family != "tests" {
		t.Errorf("Family: got %q, want tests", d.Family)
	}
	wantOutputs := map[code_framework.EntityKind]bool{
		code_framework.KindTest:         true,
		code_framework.KindFixture:      true,
		code_framework.KindContractTest: true,
	}
	for _, k := range d.Outputs {
		if !wantOutputs[k] {
			t.Errorf("unexpected output kind %q", k)
		}
		delete(wantOutputs, k)
	}
	if len(wantOutputs) != 0 {
		t.Errorf("missing output kinds: %v", wantOutputs)
	}
}

func testEmitFromFixture(t *testing.T) {
	ws := t.TempDir()
	copyFixture(t, "testdata/sample_test.go.txt", filepath.Join(ws, "sample_test.go"))

	e, err := New(code_framework.Deps{Workspace: ws})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	payload, _ := json.Marshal(map[string]any{
		"path":     "sample_test.go",
		"language": "go",
	})
	in := kernel.Event{
		Layer:   "code.core",
		Kind:    "FileChanged",
		Payload: payload,
		Seq:     7,
	}

	out, err := e.OnEvent(context.Background(), in)
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("OnEvent returned 0 events; want >=4 (3 tests + 3 sub-tests of TestCancelOrder)")
	}

	// Collect Test rows.
	names := []string{}
	subjectByName := map[string]string{}
	for _, ev := range out {
		if ev.Kind != "TestAdded" {
			continue
		}
		var test code_framework.Test
		if err := json.Unmarshal(ev.Payload, &test); err != nil {
			t.Fatalf("unmarshal Test payload: %v", err)
		}
		if test.Framework != "go_test" {
			t.Errorf("Test.Framework: got %q, want go_test", test.Framework)
		}
		names = append(names, test.Name)
		// Track subject inference for the top-level tests.
		if len(test.SubjectRef.Anchors) > 0 {
			subjectByName[test.Name] = test.SubjectRef.Anchors[0].Value
		}
	}
	sort.Strings(names)

	wantNames := []string{
		"TestCancelOrder",
		"TestCancelOrder/already-cancelled",
		"TestCancelOrder/happy",
		"TestCancelOrder/not-found",
		"TestOrderCreatedEvent",
		"TestPlaceOrder",
	}
	if len(names) != len(wantNames) {
		t.Errorf("test names: got %d (%v), want %d (%v)", len(names), names, len(wantNames), wantNames)
	}
	for i, want := range wantNames {
		if i >= len(names) {
			break
		}
		if names[i] != want {
			t.Errorf("test name [%d]: got %q, want %q", i, names[i], want)
		}
	}

	// Subject inference: TestPlaceOrder should fuzzy-match against
	// a qualified name containing "PlaceOrder" (we put orders.NewPlaceOrder
	// in the fixture). The contract allows any qn whose tail substring-
	// matches the test name.
	subj, ok := subjectByName["TestPlaceOrder"]
	if !ok {
		t.Errorf("TestPlaceOrder: missing subject inference (allowed but unexpected for this fixture)")
	} else if !containsCase(subj, "PlaceOrder") {
		t.Errorf("TestPlaceOrder subject: got %q, want something containing PlaceOrder", subj)
	}

	// Counters: TestCancelOrder must emit at least 3 sub-test rows.
	subCount := 0
	for _, n := range names {
		if len(n) > len("TestCancelOrder/") && n[:len("TestCancelOrder/")] == "TestCancelOrder/" {
			subCount++
		}
	}
	if subCount != 3 {
		t.Errorf("TestCancelOrder sub-tests: got %d, want 3", subCount)
	}
}

func testIgnoresNonTestFiles(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "regular.go"), []byte("package x\nfunc Foo(){}"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, _ := New(code_framework.Deps{Workspace: ws})
	payload, _ := json.Marshal(map[string]any{"path": "regular.go"})
	out, err := e.OnEvent(context.Background(), kernel.Event{Layer: "code.core", Kind: "FileChanged", Payload: payload})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("regular.go produced %d events; want 0", len(out))
	}
}

// copyFixture reads a testdata file and writes its contents to dst.
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

func containsCase(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			a := haystack[i+j]
			b := needle[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
