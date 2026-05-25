package code_framework

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// EntityKind constants — match schema.entity_kinds in
// internal/kernel/embedded_manifests/code.framework.yaml. The
// extractor_test.go gate enforces 1:1 correspondence.
const (
	KindRoute             EntityKind = "Route"
	KindHandler           EntityKind = "Handler"
	KindEventPublisher    EntityKind = "EventPublisher"
	KindEventSubscriber   EntityKind = "EventSubscriber"
	KindEvent             EntityKind = "Event"
	KindSchema            EntityKind = "Schema"
	KindSchemaField       EntityKind = "SchemaField"
	KindSchemaRead        EntityKind = "SchemaRead"
	KindSchemaWrite       EntityKind = "SchemaWrite"
	KindMigration         EntityKind = "Migration"
	KindTest              EntityKind = "Test"
	KindFixture           EntityKind = "Fixture"
	KindContractTest      EntityKind = "ContractTest"
	KindGraphQLOperation  EntityKind = "GraphQLOperation"
	KindMutation          EntityKind = "Mutation"
	// Job / QueueConsumer / ConfigKey are part of the code.framework
	// entity vocabulary (SPEC §2.3) and are declared in the manifest +
	// type set so selectors and the pipeline can reference them, but
	// the v1 extractor set ships no producer for them yet — they are
	// RESERVED for a post-v1 extractor (cron/scheduler, worker-queue,
	// and config-key extractors). The struct shapes + writer projection
	// cases exist so adding those extractors is additive.
	KindJob               EntityKind = "Job"
	KindQueueConsumer     EntityKind = "QueueConsumer"
	KindConfigKey         EntityKind = "ConfigKey"
	KindGeneratedArtifact EntityKind = "GeneratedArtifact"
)

// AllEntityKinds is the canonical ordered list used by manifest
// round-trip tests and by the registry to validate Outputs().
var AllEntityKinds = []EntityKind{
	KindRoute, KindHandler,
	KindEventPublisher, KindEventSubscriber, KindEvent,
	KindSchema, KindSchemaField, KindSchemaRead, KindSchemaWrite, KindMigration,
	KindTest, KindFixture, KindContractTest,
	KindGraphQLOperation, KindMutation,
	KindJob, KindQueueConsumer, KindConfigKey,
	KindGeneratedArtifact,
}

// EmittedEventKinds is the canonical ordered list of event kinds
// declared in the manifest's events.emits. Used by the gate test to
// enforce that every kind named here appears in the YAML and vice
// versa.
var EmittedEventKinds = []string{
	"RouteAdded", "RouteChanged", "RouteRemoved", "HandlerBound",
	"EventPublisherAdded", "EventPublisherRemoved",
	"EventSubscriberAdded", "EventSubscriberRemoved",
	"EventTopicObserved",
	"SchemaAdded", "SchemaRemoved",
	"SchemaFieldAdded", "SchemaFieldChanged", "SchemaFieldRemoved",
	"SchemaReadAdded", "SchemaWriteAdded",
	"MigrationAdded",
	"TestAdded", "TestChanged", "TestRemoved",
	"FixtureAdded",
	"ContractTestAdded", "ContractTestRemoved",
	"GraphQLOperationAdded", "GraphQLOperationChanged", "GraphQLOperationRemoved",
	"MutationAdded",
	"JobAdded", "QueueConsumerAdded", "ConfigKeyAdded",
	"GeneratedArtifactAdded", "UnverifiedGeneratedArtifact",
	"ExtractorRunFailed",
}

// SourceExtractorFramework is the kernel SourceClass prefix every
// framework-extractor emission carries. The per-extractor name is
// appended ("extractor:framework:" + Name) at dispatch time.
const SourceExtractorFramework kernel.SourceClass = "extractor:framework"

// Anchor is one entry on a SelectorRef's anchor list. Mirrors
// dsl.Anchor in shape but with a flat string value so extractor
// payloads do not depend on the DSL package.
type Anchor struct {
	Kind  string `json:"kind"`  // "qualified_name" | "path_glob" | "entity_kind" | "function_signature" | ...
	Value string `json:"value"` // canonical string form (numbers/floats stringified)
}

// SelectorRef is the closed-form anchor list an extractor uses to
// reference a code.core entity (SPEC §2.2). The Dispatcher resolves
// SelectorRefs through the EntityRefCache; payloads carry the anchor
// list verbatim so the §2.2 invariant (selectors are the only legal
// cross-layer reference) is visible by inspection of the event log.
type SelectorRef struct {
	Anchors []Anchor `json:"anchors"`
	Unique  bool     `json:"unique,omitempty"`
}

// Hash returns the canonical sha256 fingerprint of the SelectorRef
// for use as an EntityRefCache key.
func (s SelectorRef) Hash() [32]byte {
	// Canonicalize: sort anchors by Kind then Value so payload-order
	// differences do not affect the hash.
	anchors := make([]Anchor, len(s.Anchors))
	copy(anchors, s.Anchors)
	sort.SliceStable(anchors, func(i, j int) bool {
		if anchors[i].Kind != anchors[j].Kind {
			return anchors[i].Kind < anchors[j].Kind
		}
		return anchors[i].Value < anchors[j].Value
	})
	buf, _ := json.Marshal(struct {
		Anchors []Anchor `json:"anchors"`
		Unique  bool     `json:"unique"`
	}{Anchors: anchors, Unique: s.Unique})
	return sha256.Sum256(buf)
}

// Provenance is the per-fact provenance row attached to every
// framework-layer entity. Same shape as kernel.Provenance but with
// code.framework's source_class enum pre-filled at emit time.
type Provenance struct {
	Confidence  float64                 `json:"confidence"`
	Freshness   kernel.FreshnessClass   `json:"freshness"`
	SourceClass []kernel.SourceClass    `json:"source_class"`
	ProducedBy  string                  `json:"produced_by"` // "extractor:framework:" + Name()
	ProducedSeq uint64                  `json:"produced_seq"`
	Inputs      []string                `json:"inputs,omitempty"`
}

// ContentID is a layer-local opaque id (SPEC §4.1). For
// code.framework entities the id is built deterministically as
// ContentID(kind, anchored_to, family_attrs) so equal-content
// emissions collapse to the same row (suppress-at-source, §6.21).
type ContentID = string

// MakeContentID builds the deterministic content-id for an entity
// payload. Callers pass the canonical attribute slice (already
// sorted in a stable order).
func MakeContentID(kind EntityKind, anchor SelectorRef, attrs map[string]any) ContentID {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	canon := struct {
		Kind   EntityKind     `json:"kind"`
		Anchor [32]byte       `json:"anchor"`
		Attrs  map[string]any `json:"attrs"`
	}{Kind: kind, Anchor: anchor.Hash()}
	canon.Attrs = make(map[string]any, len(keys))
	for _, k := range keys {
		canon.Attrs[k] = attrs[k]
	}
	buf, _ := json.Marshal(canon)
	sum := sha256.Sum256(buf)
	return string(kind) + ":" + hex.EncodeToString(sum[:16])
}

// ---- Routes / Handlers -----------------------------------------------------

// Route is an HTTP route extracted from a framework router (chi,
// gin, gorilla/mux, net/http, express, fastify, django, fastapi,
// flask).
type Route struct {
	ID           ContentID   `json:"id"`
	Kind         EntityKind  `json:"kind"`
	Method       string      `json:"method"`
	PathPattern  string      `json:"path_pattern"`
	Framework    string      `json:"framework"`
	Middleware   []string    `json:"middleware,omitempty"`
	ResponseKind string      `json:"response_kind,omitempty"`
	AnchoredTo   SelectorRef `json:"anchored_to"`
	Provenance   Provenance  `json:"provenance"`
}

// Handler is the function/method bound to a Route via the `handles`
// relation. Carried as its own entity so cross-route handler reuse
// resolves to one Handler row.
type Handler struct {
	ID         ContentID   `json:"id"`
	Kind       EntityKind  `json:"kind"`
	AnchoredTo SelectorRef `json:"anchored_to"`
	Provenance Provenance  `json:"provenance"`
}

// ---- Events ----------------------------------------------------------------

// Event is the topic-level entity: one row per unique
// (transport, name). Publishers and Subscribers attach via the
// `publishes` / `subscribes_to` relations.
type Event struct {
	ID         ContentID   `json:"id"`
	Kind       EntityKind  `json:"kind"`
	Name       string      `json:"name"`
	Transport  string      `json:"transport"`
	AnchoredTo SelectorRef `json:"anchored_to,omitempty"`
	Provenance Provenance  `json:"provenance"`
}

// EventPublisher is one publication site in code.core.
type EventPublisher struct {
	ID         ContentID   `json:"id"`
	Kind       EntityKind  `json:"kind"`
	EventName  string      `json:"event_name"`
	Service    string      `json:"service,omitempty"`
	Transport  string      `json:"transport"`
	AnchoredTo SelectorRef `json:"anchored_to"`
	Provenance Provenance  `json:"provenance"`
}

// EventSubscriber is one subscription site in code.core.
type EventSubscriber struct {
	ID         ContentID   `json:"id"`
	Kind       EntityKind  `json:"kind"`
	EventName  string      `json:"event_name"`
	Service    string      `json:"service,omitempty"`
	Transport  string      `json:"transport"`
	AnchoredTo SelectorRef `json:"anchored_to"`
	Provenance Provenance  `json:"provenance"`
}

// ---- Schemas ---------------------------------------------------------------

// Schema is a database table or document type definition.
type Schema struct {
	ID         ContentID   `json:"id"`
	Kind       EntityKind  `json:"kind"`
	Table      string      `json:"table"`
	Dialect    string      `json:"dialect"`
	AnchoredTo SelectorRef `json:"anchored_to"`
	Provenance Provenance  `json:"provenance"`
}

// SchemaField is one column / field of a Schema.
type SchemaField struct {
	ID         ContentID   `json:"id"`
	Kind       EntityKind  `json:"kind"`
	SchemaID   ContentID   `json:"schema_id"`
	Name       string      `json:"name"`
	DataType   string      `json:"data_type"`
	Nullable   bool        `json:"nullable"`
	AnchoredTo SelectorRef `json:"anchored_to"`
	Provenance Provenance  `json:"provenance"`
}

// SchemaRead is one read site (SELECT / .find / .query) bound to a
// SchemaField via the `reads` relation.
type SchemaRead struct {
	ID         ContentID   `json:"id"`
	Kind       EntityKind  `json:"kind"`
	FieldID    ContentID   `json:"field_id"`
	AnchoredTo SelectorRef `json:"anchored_to"`
	Provenance Provenance  `json:"provenance"`
}

// SchemaWrite is one write site (INSERT / UPDATE / .create / .save).
type SchemaWrite struct {
	ID         ContentID   `json:"id"`
	Kind       EntityKind  `json:"kind"`
	FieldID    ContentID   `json:"field_id"`
	AnchoredTo SelectorRef `json:"anchored_to"`
	Provenance Provenance  `json:"provenance"`
}

// Migration is one schema-evolution file (goose, alembic,
// prisma migrate, drizzle kit).
type Migration struct {
	ID         ContentID   `json:"id"`
	Kind       EntityKind  `json:"kind"`
	Tool       string      `json:"tool"`
	Version    string      `json:"version"`
	AnchoredTo SelectorRef `json:"anchored_to"`
	Provenance Provenance  `json:"provenance"`
}

// ---- Tests -----------------------------------------------------------------

// Test is one test function / case detected by go test, jest, vitest,
// or pytest.
type Test struct {
	ID         ContentID   `json:"id"`
	Kind       EntityKind  `json:"kind"`
	Name       string      `json:"name"`
	Framework  string      `json:"framework"`
	SubjectRef SelectorRef `json:"subject_ref,omitempty"`
	AnchoredTo SelectorRef `json:"anchored_to"`
	Provenance Provenance  `json:"provenance"`
}

// Fixture is a reusable test fixture (pytest fixture, jest beforeEach
// helper).
type Fixture struct {
	ID         ContentID   `json:"id"`
	Kind       EntityKind  `json:"kind"`
	AnchoredTo SelectorRef `json:"anchored_to"`
	Provenance Provenance  `json:"provenance"`
}

// ContractTest is a test that exercises a Route or Event contract.
// The `tests` relation links it to the subject.
type ContractTest struct {
	ID         ContentID   `json:"id"`
	Kind       EntityKind  `json:"kind"`
	TopicName  string      `json:"topic_name,omitempty"`
	RouteRef   SelectorRef `json:"route_ref,omitempty"`
	AnchoredTo SelectorRef `json:"anchored_to"`
	Provenance Provenance  `json:"provenance"`
}

// ---- GraphQL ---------------------------------------------------------------

// GraphQLOperation is one query/mutation/subscription operation defined
// in the workspace (schema-side and operation-side both produce rows;
// the `resolver_ref` SelectorRef points at the implementing function).
type GraphQLOperation struct {
	ID            ContentID   `json:"id"`
	Kind          EntityKind  `json:"kind"`
	OperationType string      `json:"operation_type"` // "query"|"mutation"|"subscription"
	Name          string      `json:"name"`
	Arguments     []string    `json:"arguments,omitempty"`
	ReturnType    string      `json:"return_type,omitempty"`
	ResolverRef   SelectorRef `json:"resolver_ref"`
	AnchoredTo    SelectorRef `json:"anchored_to"`
	Provenance    Provenance  `json:"provenance"`
}

// Mutation is a separately-emitted alias for GraphQLOperation with
// operation_type="mutation" — preserved so callers can filter by
// kind without parsing operation_type.
type Mutation struct {
	ID          ContentID   `json:"id"`
	Kind        EntityKind  `json:"kind"`
	Name        string      `json:"name"`
	ResolverRef SelectorRef `json:"resolver_ref"`
	AnchoredTo  SelectorRef `json:"anchored_to"`
	Provenance  Provenance  `json:"provenance"`
}

// ---- Jobs / Queues / Config ------------------------------------------------

// Job is a scheduled task (cron / time-based trigger).
type Job struct {
	ID         ContentID   `json:"id"`
	Kind       EntityKind  `json:"kind"`
	Name       string      `json:"name"`
	Schedule   string      `json:"schedule,omitempty"`
	AnchoredTo SelectorRef `json:"anchored_to"`
	Provenance Provenance  `json:"provenance"`
}

// QueueConsumer is a worker bound to a queue/topic transport.
type QueueConsumer struct {
	ID         ContentID   `json:"id"`
	Kind       EntityKind  `json:"kind"`
	Queue      string      `json:"queue"`
	Transport  string      `json:"transport"`
	AnchoredTo SelectorRef `json:"anchored_to"`
	Provenance Provenance  `json:"provenance"`
}

// ConfigKey is one configuration key read from the codebase
// (env var name, config-file key, feature flag).
type ConfigKey struct {
	ID         ContentID   `json:"id"`
	Kind       EntityKind  `json:"kind"`
	Key        string      `json:"key"`
	Default    string      `json:"default,omitempty"`
	AnchoredTo SelectorRef `json:"anchored_to"`
	Provenance Provenance  `json:"provenance"`
}

// ---- Generated artifacts ---------------------------------------------------

// GeneratedArtifact is a file the workspace identifies as generated.
// `sentinel` records the header line that flagged it (or the manifest
// pattern that matched). Suppress-at-source: edits to generated files
// produce UnverifiedGeneratedArtifact events when the sentinel is
// missing (plan §5 risk row).
type GeneratedArtifact struct {
	ID         ContentID   `json:"id"`
	Kind       EntityKind  `json:"kind"`
	Path       string      `json:"path"`
	Generator  string      `json:"generator,omitempty"`
	Sentinel   string      `json:"sentinel,omitempty"`
	AnchoredTo SelectorRef `json:"anchored_to"`
	Provenance Provenance  `json:"provenance"`
}
