package anchors

import (
	"context"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/code_core"
)

func TestEventName_MatchesAcrossEventFamily(t *testing.T) {
	store := newTestStore(t)
	putEntity(t, store, code_core.Entity{
		ID: "ev1", Kind: "Event", QualifiedName: "OrderCreated",
	})
	putEntity(t, store, code_core.Entity{
		ID: "pub1", Kind: "EventPublisher", QualifiedName: "OrderCreated",
	})
	putEntity(t, store, code_core.Entity{
		ID: "sub1", Kind: "EventSubscriber", QualifiedName: "OrderCreated",
	})
	// Different event name — must be filtered out.
	putEntity(t, store, code_core.Entity{
		ID: "ev2", Kind: "Event", QualifiedName: "OrderPaid",
	})

	matches, err := EventName{}.Evaluate(context.Background(), strAnchor("event_name", "OrderCreated"), store)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(matches) != 3 {
		t.Fatalf("got %d matches, want 3: %+v", len(matches), matches)
	}
	for _, m := range matches {
		if m.QualifiedName != "OrderCreated" {
			t.Errorf("unexpected match qn=%q", m.QualifiedName)
		}
		if m.Confidence != ConfidenceEventName {
			t.Errorf("confidence = %v, want %v", m.Confidence, ConfidenceEventName)
		}
	}
}

func TestTopicName_MatchesTopicBearingEvents(t *testing.T) {
	store := newTestStore(t)
	putEntity(t, store, code_core.Entity{
		ID: "kafka1", Kind: "Event", QualifiedName: "orders.created", KindTag: "topic:kafka",
	})
	putEntity(t, store, code_core.Entity{
		ID: "nats1", Kind: "Event", QualifiedName: "orders.created", KindTag: "topic:nats",
	})
	// Plain Event without topic prefix — must be ignored.
	putEntity(t, store, code_core.Entity{
		ID: "plain", Kind: "Event", QualifiedName: "orders.created",
	})

	matches, err := TopicName{}.Evaluate(context.Background(), strAnchor("topic_name", "orders.created"), store)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	gotIDs := map[string]bool{}
	for _, m := range matches {
		gotIDs[m.EntityID] = true
		if m.Confidence != ConfidenceTopicName {
			t.Errorf("confidence = %v, want %v", m.Confidence, ConfidenceTopicName)
		}
	}
	if !gotIDs["kafka1"] || !gotIDs["nats1"] {
		t.Errorf("expected kafka1 and nats1 to match; got %v", gotIDs)
	}
	if gotIDs["plain"] {
		t.Errorf("event without topic prefix should not match topic_name")
	}
}

func TestEventName_RegisteredAtCanonicalKindString(t *testing.T) {
	reg := Registry()
	if _, ok := reg["event_name"].(EventName); !ok {
		t.Errorf("Registry['event_name'] missing/wrong type")
	}
	if _, ok := reg["topic_name"].(TopicName); !ok {
		t.Errorf("Registry['topic_name'] missing/wrong type")
	}
}
