package redispubsubgo

import (
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/extractors/events/common/testutil"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
)

func TestRedisPubSubGoExtractsChannels(t *testing.T) {
	ws, rel := testutil.LoadFixture(t, filepath.Join("testdata", "redis.go.txt"), "redis.go")
	ext, err := New(code_framework.Deps{Workspace: ws})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events := testutil.RunExtractor(t, ext, rel, "go")
	testutil.AssertPublishers(t, events, "notifications", "deprecated.ch")
	testutil.AssertSubscribers(t, events, "notifications", "alerts", "events.*")
	testutil.AssertTransport(t, events, "redis_pubsub")
	testutil.AssertDeterministic(t, ext, rel, "go", events)
}

func TestRedisPubSubGoRegistered(t *testing.T) {
	for _, n := range code_framework.Registered() {
		if n == "events.redispubsub.go" {
			return
		}
	}
	t.Fatalf("events.redispubsub.go not registered")
}
