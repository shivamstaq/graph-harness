package kafkago

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/extractors/events/common/testutil"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
)

func TestKafkaGoExtractsTopics(t *testing.T) {
	ws, rel := testutil.LoadFixture(t, filepath.Join("testdata", "producer.go.txt"), "producer.go")
	ext, err := New(code_framework.Deps{Workspace: ws})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events := testutil.RunExtractor(t, ext, rel, "go")
	if len(events) == 0 {
		t.Fatalf("expected emissions, got none")
	}

	testutil.AssertPublishers(t, events, "order.created", "inventory.updated", "payment.captured")
	testutil.AssertSubscribers(t, events, "user.signed_in", "audit.events", "alpha.topic", "beta.topic")
	testutil.AssertTransport(t, events, "kafka")

	// Verify provenance.produced_by stamp on a publisher emission.
	for _, ev := range events {
		if ev.Kind != "EventPublisherAdded" {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		provM, _ := p["provenance"].(map[string]any)
		if provM == nil || provM["produced_by"] != "extractor:framework:events.kafka.go" {
			t.Errorf("provenance.produced_by mismatch: %v", provM)
		}
		break
	}

	testutil.AssertDeterministic(t, ext, rel, "go", events)
}

func TestKafkaGoRegistered(t *testing.T) {
	found := false
	for _, n := range code_framework.Registered() {
		if n == "events.kafka.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events.kafka.go not registered")
	}
}

func TestKafkaGoEmptyFileNoEmit(t *testing.T) {
	ws := t.TempDir()
	rel := "empty.go"
	if err := os.WriteFile(filepath.Join(ws, rel), []byte("package empty\n\nfunc Nothing() {}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	ext, _ := New(code_framework.Deps{Workspace: ws})
	events := testutil.RunExtractor(t, ext, rel, "go")
	if len(events) != 0 {
		t.Fatalf("expected no emissions, got %d", len(events))
	}
}
