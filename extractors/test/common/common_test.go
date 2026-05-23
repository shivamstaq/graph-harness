package common

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"

	_ "modernc.org/sqlite"
)

func TestExtractStringLiterals(t *testing.T) {
	src := []byte(`
	const a = "hello";
	let b = 'world';
	const c = ` + "`" + `raw` + "`" + `;
	// "comment-string" — picked up too (cheap scanner)
	`)
	got := ExtractStringLiterals(src)
	want := map[string]bool{"hello": true, "world": true, "raw": true, "comment-string": true}
	for _, s := range got {
		if !want[s] {
			t.Errorf("unexpected literal %q", s)
		}
		delete(want, s)
	}
	if len(want) != 0 {
		t.Errorf("missing literals: %v", want)
	}
}

func TestInferSubject(t *testing.T) {
	src := []byte(`
	import "example.com/orders"
	func helper() { _ = orders.PlaceOrder("x") }
	`)
	sel, conf := InferSubject("TestPlaceOrder", src)
	if conf == 0 {
		t.Fatal("InferSubject: got zero confidence; want >0")
	}
	if len(sel.Anchors) == 0 {
		t.Fatal("InferSubject: no anchors")
	}
	a := sel.Anchors[0].Value
	if !contains(a, "PlaceOrder") {
		t.Errorf("subject %q does not reference PlaceOrder", a)
	}
}

func TestInferSubjectMiss(t *testing.T) {
	src := []byte(`func helper() {}`)
	_, conf := InferSubject("TestZzzTotallyUnrelated", src)
	if conf > 0 {
		// fallback to bare ident may still produce a noisy hit; the
		// contract allows confidence in 0..0.8 with 0 = no-inference.
		// We tolerate either outcome — assertion is the call doesn't
		// panic and confidence stays within band.
		if conf > 0.8 {
			t.Errorf("confidence %.2f out of 0..0.8 band", conf)
		}
	}
}

func TestMatchContractTargets(t *testing.T) {
	rows := KnownFrameworkRows{
		EventTopics: []string{"order.created", "user.deleted"},
		RoutePaths:  []string{"/api/orders"},
	}
	src := []byte(`
	const topic = "order.created";
	const route = "/api/orders";
	const unrelated = "noise";
	`)
	matches := MatchContractTargets(src, rows)
	if len(matches) != 2 {
		t.Fatalf("matches: got %d, want 2", len(matches))
	}
	sawTopic, sawRoute := false, false
	for _, m := range matches {
		if m.TopicName == "order.created" {
			sawTopic = true
		}
		if m.RoutePath == "/api/orders" {
			sawRoute = true
		}
	}
	if !sawTopic {
		t.Errorf("missing topic match")
	}
	if !sawRoute {
		t.Errorf("missing route match")
	}
}

func TestMatchContractTargetsEmptyRows(t *testing.T) {
	// Empty known-rows → no matches, even if the src contains every
	// possible literal.
	src := []byte(`const a = "order.created"; const b = "/api/orders";`)
	got := MatchContractTargets(src, KnownFrameworkRows{})
	if len(got) != 0 {
		t.Errorf("empty rows yielded %d matches; want 0", len(got))
	}
}

func TestLoadKnownRowsFromFacts(t *testing.T) {
	// Seed an event-log Facts handle with code.framework events that
	// the loader should harvest.
	tmp := t.TempDir()
	log, err := facts.OpenEventLog(filepath.Join(tmp, "events.db"))
	if err != nil {
		t.Fatalf("OpenEventLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	payloadTopic, _ := json.Marshal(map[string]any{"name": "order.created"})
	payloadRoute, _ := json.Marshal(map[string]any{"path_pattern": "/api/orders"})
	_, err = log.Append(context.Background(), []kernel.Event{
		{Layer: "code.framework", Kind: "EventTopicObserved", Payload: payloadTopic, ProducedBy: "extractor:framework:events.go.kafka"},
		{Layer: "code.framework", Kind: "RouteAdded", Payload: payloadRoute, ProducedBy: "extractor:framework:routes.go.chi"},
		{Layer: "code.core", Kind: "FileChanged", Payload: json.RawMessage(`{}`), ProducedBy: "layer:code.core"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	f := facts.NewEventLogFacts(log, "")
	rows, err := LoadKnownRows(context.Background(), f)
	if err != nil {
		t.Fatalf("LoadKnownRows: %v", err)
	}
	if len(rows.EventTopics) != 1 || rows.EventTopics[0] != "order.created" {
		t.Errorf("topics: %v", rows.EventTopics)
	}
	if len(rows.RoutePaths) != 1 || rows.RoutePaths[0] != "/api/orders" {
		t.Errorf("routes: %v", rows.RoutePaths)
	}
}

func TestLoadKnownRowsNilFacts(t *testing.T) {
	rows, err := LoadKnownRows(context.Background(), nil)
	if err != nil {
		t.Fatalf("LoadKnownRows(nil): %v", err)
	}
	if len(rows.EventTopics) != 0 || len(rows.RoutePaths) != 0 {
		t.Errorf("nil facts: got %v", rows)
	}
}

func TestIsTestFilePredicates(t *testing.T) {
	cases := []struct {
		path   string
		goOk   bool
		tsOk   bool
		pyOk   bool
	}{
		{"foo_test.go", true, false, false},
		{"foo.test.ts", false, true, false},
		{"foo.spec.tsx", false, true, false},
		{"test_foo.py", false, false, true},
		{"foo_test.py", false, false, true},
		{"foo.py", false, false, false},
		{"foo.ts", false, false, false},
		{"foo.go", false, false, false},
	}
	for _, c := range cases {
		if IsGoTestFile(c.path) != c.goOk {
			t.Errorf("IsGoTestFile(%q) = %v, want %v", c.path, IsGoTestFile(c.path), c.goOk)
		}
		if IsTSTestFile(c.path) != c.tsOk {
			t.Errorf("IsTSTestFile(%q) = %v, want %v", c.path, IsTSTestFile(c.path), c.tsOk)
		}
		if IsPyTestFile(c.path) != c.pyOk {
			t.Errorf("IsPyTestFile(%q) = %v, want %v", c.path, IsPyTestFile(c.path), c.pyOk)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
