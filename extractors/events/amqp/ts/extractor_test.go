package amqpts

import (
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/extractors/events/common/testutil"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
)

func TestAMQPTSExtractsTopics(t *testing.T) {
	ws, rel := testutil.LoadFixture(t, filepath.Join("testdata", "amqp.ts.txt"), "amqp.ts")
	ext, err := New(code_framework.Deps{Workspace: ws})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events := testutil.RunExtractor(t, ext, rel, "typescript")
	testutil.AssertPublishers(t, events, "order.created", "audit.queue")
	testutil.AssertSubscribers(t, events, "orders.queue")
	testutil.AssertTransport(t, events, "amqp")
	testutil.AssertDeterministic(t, ext, rel, "typescript", events)
}

func TestAMQPTSRegistered(t *testing.T) {
	for _, n := range code_framework.Registered() {
		if n == "events.amqp.ts" {
			return
		}
	}
	t.Fatalf("events.amqp.ts not registered")
}
