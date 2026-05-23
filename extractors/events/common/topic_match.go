package common

// Topic-name matching across languages (P2.T16).
//
// v1 contract: matching is by exact string equality on the (transport,
// topic_name) tuple. A Go publisher of `kafka.Producer.WriteMessages(...,
// kafka.Message{Topic: "order.created"})` matches a TS subscriber of
// `consumer.subscribe({topic: "order.created"})` because both extractors
// stamp the same Event entity with name="order.created" and
// transport="kafka", producing the same MakeContentID, which the
// dispatcher's compare-before-emit collapses into one Event row.
//
// Limitation: this is a v1 simplification (per
// plan/02-framework-extractors.md §5 risk row + extractors/events/README.md).
// Workspaces that compute topic names from env vars, route them through
// constant-pool indirection, or use CloudEvents/AsyncAPI typed registries
// will not match without manual selector overrides. Typed-event-registry
// support (CloudEvents, AsyncAPI) is post-v1.

import "strings"

// TopicKey is the canonical (transport, name) tuple used as the
// content-addressable Event identity. Stable across language /
// framework: every events.* extractor emits Event rows whose anchor +
// attrs hash to MakeContentID(KindEvent, eventAnchor(t, n), {transport,
// name}).
type TopicKey struct {
	Transport string
	Name      string
}

// NormalizeTopic canonicalizes a topic / subject / queue name for
// cross-language matching.
//
//   - leading / trailing whitespace is trimmed (defensive against
//     fixture indentation in tests)
//   - the value is otherwise preserved verbatim — extractors are
//     responsible for stripping their own quoting / ARN prefixes
//     before calling EmitPublisher / EmitSubscriber.
func NormalizeTopic(s string) string {
	return strings.TrimSpace(s)
}

// Match reports whether two topic keys refer to the same event. v1:
// exact string equality on both transport and name.
func (k TopicKey) Match(other TopicKey) bool {
	return k.Transport == other.Transport && k.Name == other.Name
}

// ToKey makes a TopicKey from raw (transport, name).
func ToKey(transport, name string) TopicKey {
	return TopicKey{Transport: transport, Name: NormalizeTopic(name)}
}
