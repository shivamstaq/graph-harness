package code_framework

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shivamstaq/graph-harness/internal/code_core"
)

// EntityWriter projects code.framework events emitted by the
// Dispatcher into queryable code_core.Entity rows so the
// change.process pipeline's BFS (which walks code_core by Kind +
// QualifiedName per the Pass 0.5-A convention) can reach them.
//
// Wired into Dispatcher.invokeOne so writes happen synchronously
// after a successful EventLog.Append. Idempotent — code_core.PutEntity
// is upsert-by-ID, and the dispatcher's compare-before-emit
// suppression means re-emissions don't repeat the write.
//
// Linking convention (matches tests/testdata/bench/scenario2/seed.json
// and internal/change_process/pipeline.go::frameworkProducerKinds):
//
//	Event:           QualifiedName = "<transport>:<event_name>", LanguageID = "framework"
//	EventPublisher:  QualifiedName = "<transport>:<event_name>"
//	EventSubscriber: QualifiedName = "<transport>:<event_name>"
//	ContractTest:    QualifiedName = "<transport>:<topic_name>" (when topic-scoped)
//	Route:           QualifiedName = "<METHOD> <path_pattern>"
//	Handler:         QualifiedName = handler function QN (from AnchoredTo)
//	Schema:          QualifiedName = table name
//	SchemaField:     QualifiedName = "<table>.<field>"
//	SchemaRead:      QualifiedName = "<table>.<field>" (linked via field_id)
//	SchemaWrite:     QualifiedName = "<table>.<field>"
//	Test:            QualifiedName = test name
//	GraphQLOperation: QualifiedName = "<operation_type>:<name>"
//	Mutation:        QualifiedName = "mutation:<name>"
//	Migration:       QualifiedName = "<tool>:<version>"
//	GeneratedArtifact: QualifiedName = file path
//	Job:             QualifiedName = job name
//	QueueConsumer:   QualifiedName = "<transport>:<queue>"
//	ConfigKey:       QualifiedName = key
type EntityWriter interface {
	WriteFromEvent(ctx context.Context, ev EmittedEvent) error
}

// EmittedEvent is the dispatcher-internal view of an event the
// extractor returned: the Kind (e.g. "RouteAdded"), the marshalled
// payload, the inheriting Tx, and the producing extractor's name.
type EmittedEvent struct {
	Kind     string
	Payload  json.RawMessage
	Tx       string
	Producer string
	Seq      uint64
}

// codeCoreWriter is the default EntityWriter. Projects each
// EmittedEvent's payload to a code_core.Entity via the convention
// table above and PutEntity-upserts it. Also creates a reverse-index
// selector binding so the change.process BFS can reach the
// framework entity from a touched code.core entity (typically a
// function): each framework entity that has a non-empty
// `anchored_to.anchors[].value` resolvable via qualified_name gets
// a synthetic selector `framework:<entity_id>` bound to both the
// projected framework entity and the function it anchors to. The
// pipeline's Stage 4 expandTouchedViaReverseIndex then folds the
// publisher into the touched set when the function is touched.
type codeCoreWriter struct {
	store *code_core.Store
}

// NewEntityWriter constructs the default writer backed by a
// code_core.Store handle. The store MUST already be initialized
// (NewStore called on the same SQLite handle).
func NewEntityWriter(store *code_core.Store) EntityWriter {
	if store == nil {
		return nopWriter{}
	}
	return &codeCoreWriter{store: store}
}

type nopWriter struct{}

func (nopWriter) WriteFromEvent(context.Context, EmittedEvent) error { return nil }

// WriteFromEvent projects + persists a single emitted event.
//
// Returns nil for unrecognised event kinds (forward-compatible: new
// extractor families may emit kinds the writer doesn't understand
// yet — the kernel event is still durable; only the queryable row is
// skipped).
func (w *codeCoreWriter) WriteFromEvent(ctx context.Context, ev EmittedEvent) error {
	// Removal events (suffix "Removed") delete the corresponding row
	// rather than upserting. Suppress-at-source on the producer side
	// has already made the call.
	if strings.HasSuffix(ev.Kind, "Removed") {
		return w.deleteFromEvent(ctx, ev)
	}
	var generic map[string]any
	if err := json.Unmarshal(ev.Payload, &generic); err != nil {
		return nil // unrecognised shape; skip silently
	}
	id, _ := generic["id"].(string)
	if id == "" {
		return nil
	}
	kindStr, _ := generic["kind"].(string)
	if kindStr == "" {
		return nil
	}
	kind := EntityKind(kindStr)

	qn := qualifiedNameFor(kind, generic)
	if qn == "" {
		return nil
	}
	languageID := languageForProducer(ev.Producer, kind)
	seq := ev.Seq
	if seq == 0 {
		seq = 1
	}

	// Path: prefer the extractor-provided path_glob/file anchor — it
	// is the UNAMBIGUOUS file the extractor actually found this entity
	// in. Resolving the qualified_name anchor instead would be
	// ambiguous when two files declare a function with the same suffix
	// (e.g. a TS consumer and a Py consumer both named `run` would
	// both resolve to whichever `run` the suffix lookup returns first,
	// stamping both subscribers with the wrong file). The path becomes
	// the framework entity's Path so the change.process Stage-2
	// file-scoped walk marks this producer touched when its declaring
	// file is in a diff (the event-payload-mutation case).
	anchorPath := anchoredPath(generic)

	// Resolve the anchored code.core function for the reverse-index
	// binding (so a diff touching the function body reaches the
	// producer with function-level precision). Scope the lookup to the
	// anchor's file when known so the `run`-collision above doesn't
	// bind to the wrong file's function.
	var anchorEntity *code_core.Entity
	if anchorQN := anchoredQualifiedName(generic); anchorQN != "" {
		suffix := anchorQN
		if i := strings.LastIndexByte(suffix, '.'); i >= 0 {
			suffix = suffix[i+1:]
		}
		if suffix != "" {
			anchorEntity = w.resolveAnchorFunction(ctx, suffix, anchorPath)
		}
	}

	entity := code_core.Entity{
		ID:            id,
		Kind:          code_core.EntityKind(kindStr),
		LanguageID:    languageID,
		QualifiedName: qn,
	}
	switch {
	case anchorPath != "":
		entity.Path = anchorPath
	case anchorEntity != nil:
		entity.Path = anchorEntity.Path
	}
	// kind_tag carries auxiliary discriminators (method for Route,
	// transport for Event, service for Publisher/Subscriber).
	entity.KindTag = kindTagFor(kind, generic)

	if err := w.store.PutEntity(ctx, entity, seq); err != nil {
		return fmt.Errorf("framework writer PutEntity %s: %w", id, err)
	}

	// Selector binding: link this framework entity to the function it
	// anchors via a synthetic selector ID so the pipeline's
	// reverse-index walk reaches it from a touched function (same shape
	// ApplySeed uses in internal/bench/scenario2_runner.go).
	if anchorEntity != nil {
		selID := "framework:" + id
		_ = w.store.BindSelector(ctx, id, selID, "", "framework_anchor", seq)
		_ = w.store.BindSelector(ctx, anchorEntity.ID, selID, "", "qualified_name", seq)
	}
	return nil
}

// resolveAnchorFunction finds the code.core function entity the
// framework entity anchors to. When path is non-empty it scopes the
// search to that file (disambiguating same-named functions across
// files — e.g. a TS `run` and a Py `run`); otherwise it falls back to
// a workspace-wide suffix lookup.
func (w *codeCoreWriter) resolveAnchorFunction(ctx context.Context, suffix, path string) *code_core.Entity {
	if path != "" {
		ents, err := w.store.ListEntitiesByPath(ctx, path)
		if err == nil {
			for i := range ents {
				e := ents[i]
				if e.Kind == code_core.KindFile {
					continue
				}
				qn := e.QualifiedName
				if j := strings.LastIndexByte(qn, '.'); j >= 0 {
					qn = qn[j+1:]
				}
				if qn == suffix {
					return &e
				}
			}
		}
	}
	if fn, err := w.store.LookupByQualifiedNameSuffix(ctx, suffix); err == nil && fn != nil {
		return fn
	}
	return nil
}

func (w *codeCoreWriter) deleteFromEvent(ctx context.Context, ev EmittedEvent) error {
	var generic map[string]any
	if err := json.Unmarshal(ev.Payload, &generic); err != nil {
		return nil
	}
	id, _ := generic["id"].(string)
	if id == "" {
		return nil
	}
	// Best-effort drop; ignore not-found.
	_ = w.store.UnbindSelectorsForEntity(ctx, id)
	_ = w.store.DeleteEntity(ctx, id)
	return nil
}

// qualifiedNameFor implements the linking convention above for each
// entity kind.
func qualifiedNameFor(kind EntityKind, p map[string]any) string {
	get := func(k string) string {
		v, _ := p[k].(string)
		return v
	}
	switch kind {
	case KindEvent, KindEventPublisher, KindEventSubscriber:
		transport := get("transport")
		// Prefer event_name for publishers/subscribers, name for the
		// topic-level Event row.
		name := get("event_name")
		if name == "" {
			name = get("name")
		}
		if transport == "" || name == "" {
			return name
		}
		return transport + ":" + name
	case KindContractTest:
		topic := get("topic_name")
		if topic == "" {
			return get("name") // best-effort
		}
		// ContractTest links by topic; assume kafka transport by
		// default (the extractor doesn't always carry transport for
		// contract tests). If both topic and transport_hint are
		// available, prefer the qualified form.
		if t := get("transport"); t != "" {
			return t + ":" + topic
		}
		return "kafka:" + topic
	case KindRoute:
		method := get("method")
		path := get("path_pattern")
		if method == "" || path == "" {
			return path
		}
		return method + " " + path
	case KindHandler:
		// Handler QN comes from its anchor (the handler function).
		return anchoredQualifiedName(p)
	case KindSchema:
		return get("table")
	case KindSchemaField, KindSchemaRead, KindSchemaWrite:
		// SchemaField has name+schema_id; SchemaRead/Write have field_id.
		if name := get("name"); name != "" {
			// SchemaField shape: derive table.field
			if sid, ok := p["schema_id"].(string); ok && sid != "" {
				// schema_id is content-hashed; we can't reverse it
				// to a table name here, so fall back to name.
				_ = sid
			}
			return name
		}
		if fid, _ := p["field_id"].(string); fid != "" {
			return fid
		}
		return ""
	case KindMigration:
		return get("tool") + ":" + get("version")
	case KindTest, KindFixture:
		return get("name")
	case KindGraphQLOperation:
		return get("operation_type") + ":" + get("name")
	case KindMutation:
		return "mutation:" + get("name")
	case KindGeneratedArtifact:
		return get("path")
	case KindJob:
		return get("name")
	case KindQueueConsumer:
		return get("transport") + ":" + get("queue")
	case KindConfigKey:
		return get("key")
	}
	return ""
}

// kindTagFor returns a per-kind discriminator stored in
// code_entities.kind_tag. Empty when not applicable.
func kindTagFor(kind EntityKind, p map[string]any) string {
	get := func(k string) string {
		v, _ := p[k].(string)
		return v
	}
	switch kind {
	case KindRoute:
		return get("method")
	case KindEvent:
		if t := get("transport"); t != "" {
			return "topic:" + t
		}
	case KindEventPublisher, KindEventSubscriber:
		return get("service")
	case KindContractTest:
		if t := get("transport"); t != "" {
			return "topic:" + t
		}
		return "topic:kafka"
	case KindSchema:
		return get("dialect")
	case KindMigration:
		return get("tool")
	case KindTest:
		return get("framework")
	}
	return ""
}

// languageForProducer derives the source language from the
// extractor's registration name. The dotted name carries a language
// token but its POSITION varies by family — routes name themselves
// "<family>.<lang>.<variant>" (e.g. "routes.ts.express") while events
// name themselves "<family>.<transport>.<lang>" (e.g.
// "events.kafka.go"). Rather than assume a position, scan every
// segment for a known language token. Falls back to "framework" for
// cross-language join entities (Event) and "unknown" otherwise.
func languageForProducer(producer string, kind EntityKind) string {
	// Event topics are cross-language join keys; keep them on the
	// synthetic "framework" language so they don't pollute
	// per-language counts.
	if kind == KindEvent {
		return "framework"
	}
	if producer == "" {
		return "unknown"
	}
	// Strip the "extractor:framework:" prefix.
	if idx := strings.LastIndex(producer, ":"); idx >= 0 {
		producer = producer[idx+1:]
	}
	for _, seg := range strings.Split(producer, ".") {
		switch seg {
		case "go":
			return "go"
		case "ts", "typescript":
			return "typescript"
		case "py", "python":
			return "python"
		}
	}
	return "unknown"
}

// anchoredQualifiedName extracts the first qualified_name anchor's
// value from a payload's `anchored_to.anchors[]` list. Returns ""
// when no such anchor exists.
func anchoredQualifiedName(p map[string]any) string {
	at, ok := p["anchored_to"].(map[string]any)
	if !ok {
		return ""
	}
	anchors, ok := at["anchors"].([]any)
	if !ok {
		return ""
	}
	for _, a := range anchors {
		am, ok := a.(map[string]any)
		if !ok {
			continue
		}
		if k, _ := am["kind"].(string); k == "qualified_name" {
			if v, _ := am["value"].(string); v != "" {
				return v
			}
		}
	}
	return ""
}

// anchoredPath extracts the first path_glob / file anchor's value from
// a payload's anchored_to list. Used as the Path fallback for
// framework entities whose anchor is a module-level call (no enclosing
// function to resolve a qualified_name against) — e.g. a Python
// `KafkaConsumer("topic")` at import scope. Returns "" when no such
// anchor exists.
func anchoredPath(p map[string]any) string {
	at, ok := p["anchored_to"].(map[string]any)
	if !ok {
		return ""
	}
	anchors, ok := at["anchors"].([]any)
	if !ok {
		return ""
	}
	for _, a := range anchors {
		am, ok := a.(map[string]any)
		if !ok {
			continue
		}
		switch k, _ := am["kind"].(string); k {
		case "path_glob", "file":
			if v, _ := am["value"].(string); v != "" {
				return v
			}
		}
	}
	return ""
}
