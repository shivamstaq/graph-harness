package anchors

import (
	"context"
	"fmt"
	"sort"

	"github.com/shivamstaq/graph-harness/internal/dsl"
)

// Event-family anchor evaluators target code.framework Event,
// EventPublisher, EventSubscriber, and Topic-bearing Event entities.
// The code_framework EntityWriter materializes them into the shared
// code.core store with a TRANSPORT-PREFIXED qualified name so the
// change.process pipeline can link publishers and subscribers by a
// per-transport shared key:
//
//	Event           Kind="Event"          QualifiedName="<transport>:<event_name>"
//	EventPublisher  Kind="EventPublisher" QualifiedName="<transport>:<event_name>"
//	EventSubscriber Kind="EventSubscriber" QualifiedName="<transport>:<event_name>"
//	                                      KindTag="topic:<transport>" (Event only)
//
// The event_name anchor is transport-agnostic, so the evaluator
// matches against the event-name PORTION of the qualified name (the
// substring after the first ":") as well as the whole value, so a
// selector written `anchor event_name "order.created"` binds the
// "kafka:order.created" rows.

// EventName matches Event-family entities whose name equals the
// anchor's value (case-sensitive eq). Used to pin a selector to a
// single domain event across publishers, subscribers, and the event
// definition itself.
type EventName struct{}

// Kind returns "event_name".
func (EventName) Kind() string { return "event_name" }

// ConfidenceEventName is the score EventName assigns to an exact match.
// Event names are domain-specific and typically globally unique within
// a repo; confidence is high but slightly below qualified_name because
// the same name may appear on the event + publisher + subscriber trio.
const ConfidenceEventName = 0.92

// eventFamilyKinds is the canonical set of kinds an event_name anchor
// fires against. Adding a new framework-extractor variant (e.g.
// "EventConsumer") requires extending this set.
var eventFamilyKinds = map[string]struct{}{
	"Event":           {},
	"EventPublisher":  {},
	"EventSubscriber": {},
}

// Evaluate filters event-family entities by exact qualified-name
// equality. Returns the full agreement set so multi-emitter selectors
// (publisher + subscriber pairs) bind together.
func (EventName) Evaluate(ctx context.Context, a *dsl.Anchor, store Lookup) ([]Match, error) {
	want := stringValue(a)
	if want == "" {
		return nil, nil
	}
	ents, err := store.LookupAllEntities(ctx)
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, e := range ents {
		if _, ok := eventFamilyKinds[string(e.Kind)]; !ok {
			continue
		}
		if e.QualifiedName != want && eventNamePart(e.QualifiedName) != want {
			continue
		}
		out = append(out, matchFromEntity(e, ConfidenceEventName,
			fmt.Sprintf("event_name == %q", want)))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].QualifiedName != out[j].QualifiedName {
			return out[i].QualifiedName < out[j].QualifiedName
		}
		return out[i].EntityID < out[j].EntityID
	})
	return out, nil
}

// TopicName matches Event entities whose transport-topic name equals
// the anchor's value. The transport (kafka, nats, …) is encoded in
// KindTag with the "topic:<transport>" prefix; the anchor itself is
// transport-agnostic so a selector portable across brokers binds
// against every transport variant of the same topic.
type TopicName struct{}

// Kind returns "topic_name".
func (TopicName) Kind() string { return "topic_name" }

// ConfidenceTopicName is the score TopicName assigns to an exact match.
// Topic names are typically globally unique within a workspace; the
// score mirrors EventName for symmetry.
const ConfidenceTopicName = 0.92

// Evaluate filters Event entities to topic-bearing variants (KindTag
// has the "topic:" prefix) and matches their qualified name against
// the anchor value.
func (TopicName) Evaluate(ctx context.Context, a *dsl.Anchor, store Lookup) ([]Match, error) {
	want := stringValue(a)
	if want == "" {
		return nil, nil
	}
	ents, err := store.LookupAllEntities(ctx)
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, e := range ents {
		if string(e.Kind) != "Event" {
			continue
		}
		if !hasTopicPrefix(e.KindTag) {
			continue
		}
		if e.QualifiedName != want && eventNamePart(e.QualifiedName) != want {
			continue
		}
		out = append(out, matchFromEntity(e, ConfidenceTopicName,
			fmt.Sprintf("topic_name == %q", want)))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].EntityID < out[j].EntityID
	})
	return out, nil
}

// eventNamePart returns the event-name portion of a transport-prefixed
// qualified name ("kafka:order.created" → "order.created"). When the
// qualified name carries no transport prefix it is returned unchanged,
// so the evaluator stays correct for both the composite convention and
// any future bare-name rows.
func eventNamePart(qn string) string {
	for i := 0; i < len(qn); i++ {
		if qn[i] == ':' {
			return qn[i+1:]
		}
	}
	return qn
}

// hasTopicPrefix reports whether tag begins with the canonical
// "topic:" marker the Pass 1 extractor stamps onto KindTag for
// transport-bound events.
func hasTopicPrefix(tag string) bool {
	const prefix = "topic:"
	if len(tag) < len(prefix) {
		return false
	}
	return tag[:len(prefix)] == prefix
}
