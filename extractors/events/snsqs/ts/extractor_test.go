package snsqsts

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/extractors/events/common/testutil"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
)

func TestSNSQSTSExtracts(t *testing.T) {
	ws, rel := testutil.LoadFixture(t, filepath.Join("testdata", "snsqs.ts.txt"), "snsqs.ts")
	ext, err := New(code_framework.Deps{Workspace: ws})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events := testutil.RunExtractor(t, ext, rel, "typescript")
	testutil.AssertPublishers(t, events, "order-events", "orders-queue")
	testutil.AssertSubscribers(t, events, "audit-queue")

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
	if transports["order-events"] != "sns" || transports["orders-queue"] != "sqs" || transports["audit-queue"] != "sqs" {
		t.Errorf("transports mismatch: %v", transports)
	}
	testutil.AssertDeterministic(t, ext, rel, "typescript", events)
}

func TestSNSQSTSRegistered(t *testing.T) {
	for _, n := range code_framework.Registered() {
		if n == "events.snsqs.ts" {
			return
		}
	}
	t.Fatalf("events.snsqs.ts not registered")
}
