package snsqsgo

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/extractors/events/common/testutil"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
)

func TestSNSQSGoExtractsTopicsAndQueues(t *testing.T) {
	ws, rel := testutil.LoadFixture(t, filepath.Join("testdata", "snsqs.go.txt"), "snsqs.go")
	ext, err := New(code_framework.Deps{Workspace: ws})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events := testutil.RunExtractor(t, ext, rel, "go")
	testutil.AssertPublishers(t, events, "order-events", "orders-queue")
	testutil.AssertSubscribers(t, events, "audit-queue")

	// SNS and SQS sites stamp distinct transports.
	transports := map[string]string{}
	for _, ev := range events {
		var p map[string]any
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		name, _ := p["name"].(string)
		if name == "" {
			name, _ = p["event_name"].(string)
		}
		txp, _ := p["transport"].(string)
		if name != "" {
			transports[name] = txp
		}
	}
	if transports["order-events"] != "sns" {
		t.Errorf("order-events transport=%q, want sns", transports["order-events"])
	}
	if transports["orders-queue"] != "sqs" || transports["audit-queue"] != "sqs" {
		t.Errorf("SQS transports mismatch: %v", transports)
	}

	testutil.AssertDeterministic(t, ext, rel, "go", events)
}

func TestSNSQSGoRegistered(t *testing.T) {
	for _, n := range code_framework.Registered() {
		if n == "events.snsqs.go" {
			return
		}
	}
	t.Fatalf("events.snsqs.go not registered")
}
