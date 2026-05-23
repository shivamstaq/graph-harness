package common

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// ConfidenceEmit is the per-(handler-resolution-quality) confidence for
// event extractors. Mirrors the routes-Go convention so the framework
// fold sees consistent priors across families.
const (
	ConfidenceWithQualifiedName   = 0.92
	ConfidenceAnonymousSite       = 0.70
	ConfidenceEventDefinitionMeta = 0.88 // for the topic-level Event row
)

// EmitArgs is the argument bundle each per-(transport, language)
// extractor builds and hands to one of EmitPublisher / EmitSubscriber.
// The companion Event entity is emitted by Emit*; callers do not build
// it themselves.
type EmitArgs struct {
	// ExtractorName is the registered extractor name (e.g. "events.kafka.go").
	// ProducedBy stamps "extractor:framework:" + ExtractorName.
	ExtractorName string

	// Transport is one of the Transport* constants in this package.
	Transport string

	// EventName is the topic / subject string the call site references.
	// MUST be a literal value: callers drop non-literal sites at the
	// pattern matcher layer.
	EventName string

	// Path is the workspace-relative path of the source file.
	Path string

	// EnclosingQualifiedName is the canonical qualified-name of the
	// publish / subscribe call site's enclosing function. Empty if the
	// call site is at file scope; in that case the SelectorRef falls
	// back to a path_glob anchor.
	EnclosingQualifiedName string

	// Service is an optional service name for selector anchoring (e.g.
	// "audit" for the audit-consumer fixture). Carried verbatim on
	// EventPublisher.Service / EventSubscriber.Service so flows can
	// pin to a specific service.
	Service string

	// InputRefs is the upstream event/entity ids the extractor consumed
	// to produce this fact. Plumbed into Provenance.Inputs.
	InputRefs []string
}

// publishOrSubscribeAnchor builds the SelectorRef that pins a
// publisher / subscriber to its enclosing function (or file).
//
// Anchor list (in selector-priority order):
//
//  1. qualified_name → "pkg.Func" / "pkg.Recv.Method" (preferred)
//  2. event_name      → topic/subject string (selector-stable across renames)
//  3. path_glob       → workspace-relative path (fallback when qualified name missing)
//
// The Unique flag is set when a qualified_name anchor is available;
// duplicates collapse via MakeContentID otherwise.
func publishOrSubscribeAnchor(args EmitArgs) code_framework.SelectorRef {
	var anchors []code_framework.Anchor
	if args.EnclosingQualifiedName != "" {
		anchors = append(anchors, code_framework.Anchor{Kind: "qualified_name", Value: args.EnclosingQualifiedName})
	}
	if args.EventName != "" {
		anchors = append(anchors, code_framework.Anchor{Kind: "event_name", Value: args.EventName})
	}
	if args.Path != "" {
		anchors = append(anchors, code_framework.Anchor{Kind: "path_glob", Value: args.Path})
	}
	return code_framework.SelectorRef{Anchors: anchors, Unique: args.EnclosingQualifiedName != ""}
}

// eventAnchor builds the SelectorRef for the topic-level Event entity.
// The Event row is workspace-global: one row per (transport, topic).
// Anchor list:
//
//   - event_name → topic string
//   - entity_kind → "Event" (kindwise pin)
//
// No path_glob because the Event is a domain-level entity shared across
// every file that touches the topic.
func eventAnchor(transport, eventName string) code_framework.SelectorRef {
	anchors := []code_framework.Anchor{
		{Kind: "event_name", Value: eventName},
		{Kind: "entity_kind", Value: "Event"},
	}
	if transport != "" {
		anchors = append(anchors, code_framework.Anchor{Kind: "topic_transport", Value: transport})
	}
	return code_framework.SelectorRef{Anchors: anchors, Unique: true}
}

func provenance(args EmitArgs, confidence float64) code_framework.Provenance {
	return code_framework.Provenance{
		Confidence:  confidence,
		Freshness:   kernel.FreshnessLive,
		SourceClass: []kernel.SourceClass{code_framework.SourceExtractorFramework},
		ProducedBy:  "extractor:framework:" + args.ExtractorName,
		Inputs:      args.InputRefs,
	}
}

// EmitPublisher builds (Event, EventPublisher) kernel events for a
// detected publish call site. Returns nil when EventName or Transport
// is empty (the caller should drop non-literal sites at the matcher).
func EmitPublisher(args EmitArgs) ([]kernel.Event, error) {
	if args.EventName == "" || args.Transport == "" {
		return nil, nil
	}
	conf := ConfidenceAnonymousSite
	if args.EnclosingQualifiedName != "" {
		conf = ConfidenceWithQualifiedName
	}

	evEvents, err := buildEventEvent(args)
	if err != nil {
		return nil, err
	}

	pubAnchor := publishOrSubscribeAnchor(args)
	pubAttrs := map[string]any{
		"event_name": args.EventName,
		"transport":  args.Transport,
	}
	if args.Service != "" {
		pubAttrs["service"] = args.Service
	}
	pubID := code_framework.MakeContentID(code_framework.KindEventPublisher, pubAnchor, pubAttrs)
	pub := code_framework.EventPublisher{
		ID:         pubID,
		Kind:       code_framework.KindEventPublisher,
		EventName:  args.EventName,
		Service:    args.Service,
		Transport:  args.Transport,
		AnchoredTo: pubAnchor,
		Provenance: provenance(args, conf),
	}
	pubPayload, err := json.Marshal(pub)
	if err != nil {
		return nil, fmt.Errorf("marshal publisher: %w", err)
	}
	out := append(evEvents, kernel.Event{Kind: "EventPublisherAdded", Payload: pubPayload})
	return out, nil
}

// EmitSubscriber builds (Event, EventSubscriber) kernel events for a
// detected subscribe call site. Returns nil when EventName or Transport
// is empty.
func EmitSubscriber(args EmitArgs) ([]kernel.Event, error) {
	if args.EventName == "" || args.Transport == "" {
		return nil, nil
	}
	conf := ConfidenceAnonymousSite
	if args.EnclosingQualifiedName != "" {
		conf = ConfidenceWithQualifiedName
	}

	evEvents, err := buildEventEvent(args)
	if err != nil {
		return nil, err
	}

	subAnchor := publishOrSubscribeAnchor(args)
	subAttrs := map[string]any{
		"event_name": args.EventName,
		"transport":  args.Transport,
	}
	if args.Service != "" {
		subAttrs["service"] = args.Service
	}
	subID := code_framework.MakeContentID(code_framework.KindEventSubscriber, subAnchor, subAttrs)
	sub := code_framework.EventSubscriber{
		ID:         subID,
		Kind:       code_framework.KindEventSubscriber,
		EventName:  args.EventName,
		Service:    args.Service,
		Transport:  args.Transport,
		AnchoredTo: subAnchor,
		Provenance: provenance(args, conf),
	}
	subPayload, err := json.Marshal(sub)
	if err != nil {
		return nil, fmt.Errorf("marshal subscriber: %w", err)
	}
	out := append(evEvents, kernel.Event{Kind: "EventSubscriberAdded", Payload: subPayload})
	return out, nil
}

// buildEventEvent produces the topic-level Event kernel.Event for a
// (transport, name) pair. The Event row is unique by (transport, name)
// across the workspace; MakeContentID is identical for repeated
// emissions so the Dispatcher's compare-before-emit collapses them.
func buildEventEvent(args EmitArgs) ([]kernel.Event, error) {
	anchor := eventAnchor(args.Transport, args.EventName)
	attrs := map[string]any{
		"name":      args.EventName,
		"transport": args.Transport,
	}
	id := code_framework.MakeContentID(code_framework.KindEvent, anchor, attrs)
	ev := code_framework.Event{
		ID:         id,
		Kind:       code_framework.KindEvent,
		Name:       args.EventName,
		Transport:  args.Transport,
		AnchoredTo: anchor,
		Provenance: provenance(args, ConfidenceEventDefinitionMeta),
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("marshal event: %w", err)
	}
	return []kernel.Event{{Kind: "EventTopicObserved", Payload: payload}}, nil
}

// DedupedEvents collapses a slice of kernel.Events from a single
// OnEvent invocation by the payload id field. Each unique (Kind, id)
// pair is kept; subsequent duplicates are dropped. Preserves the first
// occurrence's order for determinism.
//
// Two emission sites in the same file that target the same topic
// produce identical Event rows but distinct Publisher rows (their
// enclosing qualified_name differs). This helper coalesces the Event
// duplicates so the dispatcher only sees one per topic.
func DedupedEvents(in []kernel.Event) []kernel.Event {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]kernel.Event, 0, len(in))
	for _, ev := range in {
		key := dedupeKey(ev)
		if key == "" {
			out = append(out, ev)
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ev)
	}
	return out
}

func dedupeKey(ev kernel.Event) string {
	var p map[string]any
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return ""
	}
	id, _ := p["id"].(string)
	if id == "" {
		return ""
	}
	return ev.Kind + "/" + id
}

// SortEventsForDeterminism orders the emission slice so identical
// per-file extractions produce identical kernel.Event slices. The
// dispatcher does not require this — compare-before-emit is keyed on
// id — but tests assert deterministic outputs, so we sort here.
func SortEventsForDeterminism(in []kernel.Event) []kernel.Event {
	out := make([]kernel.Event, len(in))
	copy(out, in)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return dedupeKey(out[i]) < dedupeKey(out[j])
	})
	return out
}

// CommonInputs returns the standard input event list for a v1 events
// extractor: every code.core file-change drift event. The dispatcher
// already invalidates the EntityRefCache for these; the extractor
// re-walks the file on each.
func CommonInputs() []code_framework.EventKind {
	return []code_framework.EventKind{
		code_framework.InputCoreFileChanged,
	}
}

// CommonOutputs returns the standard output entity-kind list for an
// events extractor. Matches code.framework manifest entity_kinds.
func CommonOutputs() []code_framework.EntityKind {
	return []code_framework.EntityKind{
		code_framework.KindEvent,
		code_framework.KindEventPublisher,
		code_framework.KindEventSubscriber,
	}
}
