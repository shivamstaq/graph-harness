package redispubsubts

import (
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/extractors/events/common/testutil"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
)

func TestRedisPubSubTSExtractsChannels(t *testing.T) {
	ws, rel := testutil.LoadFixture(t, filepath.Join("testdata", "redis.ts.txt"), "redis.ts")
	ext, err := New(code_framework.Deps{Workspace: ws})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events := testutil.RunExtractor(t, ext, rel, "typescript")
	testutil.AssertPublishers(t, events, "notifications")
	testutil.AssertSubscribers(t, events, "notifications", "alerts", "events.*")
	testutil.AssertTransport(t, events, "redis_pubsub")
	testutil.AssertDeterministic(t, ext, rel, "typescript", events)
}

func TestRedisPubSubTSRegistered(t *testing.T) {
	for _, n := range code_framework.Registered() {
		if n == "events.redispubsub.ts" {
			return
		}
	}
	t.Fatalf("events.redispubsub.ts not registered")
}
