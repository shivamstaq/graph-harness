package redispubsubpy

import (
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/extractors/events/common/testutil"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
)

func TestRedisPubSubPyExtractsChannels(t *testing.T) {
	ws, rel := testutil.LoadFixture(t, filepath.Join("testdata", "redis.py.txt"), "redis.py")
	ext, err := New(code_framework.Deps{Workspace: ws})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events := testutil.RunExtractor(t, ext, rel, "python")
	testutil.AssertPublishers(t, events, "notifications")
	testutil.AssertSubscribers(t, events, "notifications", "alerts", "events.*")
	testutil.AssertTransport(t, events, "redis_pubsub")
	testutil.AssertDeterministic(t, ext, rel, "python", events)
}

func TestRedisPubSubPyRegistered(t *testing.T) {
	for _, n := range code_framework.Registered() {
		if n == "events.redispubsub.py" {
			return
		}
	}
	t.Fatalf("events.redispubsub.py not registered")
}
