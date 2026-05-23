// Package testutil exposes test helpers shared by every
// (transport, language) extractor's _test.go. Each extractor's test
// drives the full OnEvent path against a small inline fixture; the
// helpers here load fixture files into temp workspaces and assert on
// the resulting kernel.Event slice.
package testutil

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// LoadFixture writes the testdata file into a temp workspace at the
// given relative path. Returns (workspace_root, rel_path).
func LoadFixture(t *testing.T, fixturePath, relPath string) (string, string) {
	t.Helper()
	src, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	ws := t.TempDir()
	dest := filepath.Join(ws, relPath)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dest, src, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return ws, relPath
}

// NewFileChangedEvent builds a code.core.FileChanged kernel.Event
// shaped like internal/daemon.FileChangedPayload.
func NewFileChangedEvent(t *testing.T, path, language string) kernel.Event {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"path": path, "language": language})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return kernel.Event{
		Layer:   "code.core",
		Kind:    "FileChanged",
		Payload: payload,
	}
}

// RunExtractor calls e.OnEvent with a synthetic FileChanged event for
// path. Returns the emitted slice or fails the test.
func RunExtractor(t *testing.T, e code_framework.Extractor, path, language string) []kernel.Event {
	t.Helper()
	events, err := e.OnEvent(context.Background(), NewFileChangedEvent(t, path, language))
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	return events
}

// EmittedTopics returns the (kind, topic_name) pairs from a slice of
// emitted kernel events. The topic_name is read from the payload's
// "name" field (Event entity) or "event_name" field (Publisher /
// Subscriber).
func EmittedTopics(t *testing.T, events []kernel.Event) map[string]map[string]bool {
	t.Helper()
	out := make(map[string]map[string]bool)
	for _, ev := range events {
		var p map[string]any
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		topic, _ := p["name"].(string)
		if topic == "" {
			topic, _ = p["event_name"].(string)
		}
		if topic == "" {
			continue
		}
		if out[ev.Kind] == nil {
			out[ev.Kind] = map[string]bool{}
		}
		out[ev.Kind][topic] = true
	}
	return out
}

// AssertPublishers asserts that every wanted topic is present as a
// EventPublisherAdded + EventTopicObserved emission.
func AssertPublishers(t *testing.T, events []kernel.Event, want ...string) {
	t.Helper()
	got := EmittedTopics(t, events)
	for _, topic := range want {
		if !got["EventPublisherAdded"][topic] {
			t.Errorf("missing EventPublisherAdded for topic %q", topic)
		}
		if !got["EventTopicObserved"][topic] {
			t.Errorf("missing EventTopicObserved for topic %q (publisher)", topic)
		}
	}
}

// AssertSubscribers asserts the same for subscribers.
func AssertSubscribers(t *testing.T, events []kernel.Event, want ...string) {
	t.Helper()
	got := EmittedTopics(t, events)
	for _, topic := range want {
		if !got["EventSubscriberAdded"][topic] {
			t.Errorf("missing EventSubscriberAdded for topic %q", topic)
		}
		if !got["EventTopicObserved"][topic] {
			t.Errorf("missing EventTopicObserved for topic %q (subscriber)", topic)
		}
	}
}

// AssertTransport scans the emissions and verifies every Publisher /
// Subscriber payload carries transport == want.
func AssertTransport(t *testing.T, events []kernel.Event, want string) {
	t.Helper()
	for _, ev := range events {
		if ev.Kind != "EventPublisherAdded" && ev.Kind != "EventSubscriberAdded" && ev.Kind != "EventTopicObserved" {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		if got, _ := p["transport"].(string); got != want {
			t.Errorf("transport=%q on %s, want %q (payload=%s)", got, ev.Kind, want, ev.Payload)
		}
	}
}

// AssertDeterministic re-runs an extractor against the same input and
// confirms byte-identical payloads come out in the same order.
func AssertDeterministic(t *testing.T, e code_framework.Extractor, path, language string, first []kernel.Event) {
	t.Helper()
	again := RunExtractor(t, e, path, language)
	if len(first) != len(again) {
		t.Fatalf("non-deterministic emission count: %d vs %d", len(first), len(again))
	}
	for i := range first {
		if first[i].Kind != again[i].Kind {
			t.Errorf("kind mismatch at %d: %q vs %q", i, first[i].Kind, again[i].Kind)
		}
		if string(first[i].Payload) != string(again[i].Payload) {
			t.Errorf("payload mismatch at %d", i)
		}
	}
}
