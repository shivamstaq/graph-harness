package amqppy

import (
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/extractors/events/common/testutil"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
)

func TestAMQPPyExtractsTopics(t *testing.T) {
	ws, rel := testutil.LoadFixture(t, filepath.Join("testdata", "amqp.py.txt"), "amqp.py")
	ext, err := New(code_framework.Deps{Workspace: ws})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events := testutil.RunExtractor(t, ext, rel, "python")
	testutil.AssertPublishers(t, events, "order.created")
	testutil.AssertSubscribers(t, events, "orders.queue")
	testutil.AssertTransport(t, events, "amqp")
	testutil.AssertDeterministic(t, ext, rel, "python", events)
}

func TestAMQPPyRegistered(t *testing.T) {
	for _, n := range code_framework.Registered() {
		if n == "events.amqp.py" {
			return
		}
	}
	t.Fatalf("events.amqp.py not registered")
}
