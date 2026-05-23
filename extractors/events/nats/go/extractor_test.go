package natsgo

import (
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/extractors/events/common/testutil"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
)

func TestNATSGoExtractsSubjects(t *testing.T) {
	ws, rel := testutil.LoadFixture(t, filepath.Join("testdata", "nats.go.txt"), "nats.go")
	ext, err := New(code_framework.Deps{Workspace: ws})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events := testutil.RunExtractor(t, ext, rel, "go")
	testutil.AssertPublishers(t, events, "hello.greeting", "order.created")
	testutil.AssertSubscribers(t, events, "hello.greeting", "worker.jobs")
	testutil.AssertTransport(t, events, "nats")
	testutil.AssertDeterministic(t, ext, rel, "go", events)
}

func TestNATSGoRegistered(t *testing.T) {
	found := false
	for _, n := range code_framework.Registered() {
		if n == "events.nats.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events.nats.go not registered")
	}
}
