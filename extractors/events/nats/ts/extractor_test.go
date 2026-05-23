package natsts

import (
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/extractors/events/common/testutil"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
)

func TestNATSTSExtractsSubjects(t *testing.T) {
	ws, rel := testutil.LoadFixture(t, filepath.Join("testdata", "nats.ts.txt"), "nats.ts")
	ext, err := New(code_framework.Deps{Workspace: ws})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events := testutil.RunExtractor(t, ext, rel, "typescript")
	testutil.AssertPublishers(t, events, "hello.greeting", "ping.echo")
	testutil.AssertSubscribers(t, events, "hello.greeting")
	testutil.AssertTransport(t, events, "nats")
	testutil.AssertDeterministic(t, ext, rel, "typescript", events)
}

func TestNATSTSRegistered(t *testing.T) {
	for _, n := range code_framework.Registered() {
		if n == "events.nats.ts" {
			return
		}
	}
	t.Fatalf("events.nats.ts not registered")
}
