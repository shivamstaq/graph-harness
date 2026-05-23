package kafkats

import (
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/extractors/events/common/testutil"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
)

func TestKafkaTSExtractsTopics(t *testing.T) {
	ws, rel := testutil.LoadFixture(t, filepath.Join("testdata", "producer.ts.txt"), "producer.ts")
	ext, err := New(code_framework.Deps{Workspace: ws})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events := testutil.RunExtractor(t, ext, rel, "typescript")
	testutil.AssertPublishers(t, events, "order.created", "alpha.events", "beta.events")
	testutil.AssertSubscribers(t, events, "order.created", "alpha.events", "beta.events")
	testutil.AssertTransport(t, events, "kafka")
	testutil.AssertDeterministic(t, ext, rel, "typescript", events)
}

func TestKafkaTSRegistered(t *testing.T) {
	for _, n := range code_framework.Registered() {
		if n == "events.kafka.ts" {
			return
		}
	}
	t.Fatalf("events.kafka.ts not registered")
}
