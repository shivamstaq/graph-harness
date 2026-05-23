package kafkapy

import (
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/extractors/events/common/testutil"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
)

func TestKafkaPyExtractsTopics(t *testing.T) {
	ws, rel := testutil.LoadFixture(t, filepath.Join("testdata", "producer.py.txt"), "producer.py")
	ext, err := New(code_framework.Deps{Workspace: ws})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events := testutil.RunExtractor(t, ext, rel, "python")
	testutil.AssertPublishers(t, events, "order.created", "inventory.updated")
	testutil.AssertSubscribers(t, events, "order.created", "audit.events", "user.signed_in")
	testutil.AssertTransport(t, events, "kafka")
	testutil.AssertDeterministic(t, ext, rel, "python", events)
}

func TestKafkaPyRegistered(t *testing.T) {
	for _, n := range code_framework.Registered() {
		if n == "events.kafka.py" {
			return
		}
	}
	t.Fatalf("events.kafka.py not registered")
}
