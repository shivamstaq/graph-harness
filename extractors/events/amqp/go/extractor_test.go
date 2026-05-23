package amqpgo

import (
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/extractors/events/common/testutil"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
)

func TestAMQPGoExtractsTopics(t *testing.T) {
	ws, rel := testutil.LoadFixture(t, filepath.Join("testdata", "amqp.go.txt"), "amqp.go")
	ext, err := New(code_framework.Deps{Workspace: ws})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events := testutil.RunExtractor(t, ext, rel, "go")
	testutil.AssertPublishers(t, events, "order.created", "billing.invoiced")
	testutil.AssertSubscribers(t, events, "orders.queue")
	testutil.AssertTransport(t, events, "amqp")
	testutil.AssertDeterministic(t, ext, rel, "go", events)
}

func TestAMQPGoRegistered(t *testing.T) {
	for _, n := range code_framework.Registered() {
		if n == "events.amqp.go" {
			return
		}
	}
	t.Fatalf("events.amqp.go not registered")
}
