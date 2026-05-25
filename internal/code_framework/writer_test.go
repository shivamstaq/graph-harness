package code_framework

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/code_core"

	_ "modernc.org/sqlite" // SQLite driver
)

func newWriterTestStore(t *testing.T) *code_core.Store {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "code.core.db") +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := code_core.NewStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

// emit marshals a framework entity payload into the EmittedEvent shape
// the dispatcher hands the writer.
func emit(t *testing.T, kind, producer string, payload any) EmittedEvent {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return EmittedEvent{Kind: kind, Payload: b, Producer: producer, Seq: 1}
}

// TestEntityWriter_EventPublisherProjection is the GAP-006 gate: an
// EventPublisher emission must project to a queryable code_core row
// with the transport-prefixed qualified name, the anchored function's
// file path, and a reverse-index binding tying the publisher to its
// function.
func TestEntityWriter_EventPublisherProjection(t *testing.T) {
	store := newWriterTestStore(t)
	ctx := context.Background()

	// Seed the anchor function (the publisher's enclosing func) as the
	// cold sweep would have.
	if err := store.PutEntity(ctx, code_core.Entity{
		ID:            "fn:order.PublishOrderCreated",
		Kind:          code_core.KindFunction,
		LanguageID:    "go",
		QualifiedName: "order.PublishOrderCreated",
		Path:          "services/order/publisher.go",
	}, 1); err != nil {
		t.Fatalf("seed function: %v", err)
	}

	w := NewEntityWriter(store)
	pub := EventPublisher{
		ID:        "EventPublisher:abc",
		Kind:      KindEventPublisher,
		EventName: "order.created",
		Transport: "kafka",
		AnchoredTo: SelectorRef{Anchors: []Anchor{
			{Kind: "qualified_name", Value: "order.PublishOrderCreated"},
			{Kind: "path_glob", Value: "services/order/publisher.go"},
		}},
	}
	if err := w.WriteFromEvent(ctx, emit(t, "EventPublisherAdded", "extractor:framework:events.kafka.go", pub)); err != nil {
		t.Fatalf("WriteFromEvent: %v", err)
	}

	// The projected row must be queryable by its transport-prefixed QN.
	got, err := store.LookupEntityByID(ctx, "EventPublisher:abc")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got == nil {
		t.Fatal("EventPublisher row not materialized")
	}
	if got.QualifiedName != "kafka:order.created" {
		t.Errorf("QualifiedName: got %q, want %q", got.QualifiedName, "kafka:order.created")
	}
	if string(got.Kind) != "EventPublisher" {
		t.Errorf("Kind: got %q, want EventPublisher", got.Kind)
	}
	if got.LanguageID != "go" {
		t.Errorf("LanguageID: got %q, want go (events.kafka.go → lang is last segment)", got.LanguageID)
	}
	if got.Path != "services/order/publisher.go" {
		t.Errorf("Path: got %q, want the anchored function's file", got.Path)
	}

	// The reverse index must bind the publisher's synthetic selector to
	// the anchor function, so a diff touching the function reaches the
	// publisher (the pipeline's expandTouchedViaReverseIndex path).
	bindings, err := store.SelectorsBoundTo(ctx, "fn:order.PublishOrderCreated")
	if err != nil {
		t.Fatalf("SelectorsBoundTo: %v", err)
	}
	foundSel := ""
	for _, b := range bindings {
		if b.SelectorID == "framework:EventPublisher:abc" {
			foundSel = b.SelectorID
		}
	}
	if foundSel == "" {
		t.Fatal("no reverse-index binding from the anchor function to the publisher selector")
	}
	peers, err := store.EntitiesForSelector(ctx, foundSel)
	if err != nil {
		t.Fatalf("EntitiesForSelector: %v", err)
	}
	hasPub, hasFn := false, false
	for _, p := range peers {
		switch p {
		case "EventPublisher:abc":
			hasPub = true
		case "fn:order.PublishOrderCreated":
			hasFn = true
		}
	}
	if !hasPub || !hasFn {
		t.Errorf("selector should bind both publisher and function; got peers %v", peers)
	}
}

// TestEntityWriter_PathDisambiguatesSameNamedFunctions is the GAP-001
// regression: two subscribers whose enclosing functions share a name
// (`run`) across different files must each get THEIR file's path, not
// whichever the suffix lookup returns first. The fix prefers the
// extractor's path_glob anchor over the ambiguous qualified_name
// suffix lookup.
func TestEntityWriter_PathDisambiguatesSameNamedFunctions(t *testing.T) {
	store := newWriterTestStore(t)
	ctx := context.Background()
	for _, f := range []struct{ id, qn, path string }{
		{"fn:web.run", "web.run", "apps/web/consumer.ts"},
		{"fn:audit.run", "audit.run", "pipelines/audit/consumer.py"},
	} {
		if err := store.PutEntity(ctx, code_core.Entity{
			ID: f.id, Kind: code_core.KindFunction, QualifiedName: f.qn, Path: f.path,
		}, 1); err != nil {
			t.Fatalf("seed %s: %v", f.id, err)
		}
	}
	w := NewEntityWriter(store)

	tsSub := EventSubscriber{
		ID: "EventSubscriber:ts", Kind: KindEventSubscriber, EventName: "order.created", Transport: "kafka",
		AnchoredTo: SelectorRef{Anchors: []Anchor{
			{Kind: "qualified_name", Value: "web.run"},
			{Kind: "path_glob", Value: "apps/web/consumer.ts"},
		}},
	}
	pySub := EventSubscriber{
		ID: "EventSubscriber:py", Kind: KindEventSubscriber, EventName: "order.created", Transport: "kafka",
		AnchoredTo: SelectorRef{Anchors: []Anchor{
			{Kind: "qualified_name", Value: "audit.run"},
			{Kind: "path_glob", Value: "pipelines/audit/consumer.py"},
		}},
	}
	if err := w.WriteFromEvent(ctx, emit(t, "EventSubscriberAdded", "extractor:framework:events.kafka.ts", tsSub)); err != nil {
		t.Fatalf("write ts: %v", err)
	}
	if err := w.WriteFromEvent(ctx, emit(t, "EventSubscriberAdded", "extractor:framework:events.kafka.py", pySub)); err != nil {
		t.Fatalf("write py: %v", err)
	}

	cases := map[string]struct{ path, lang string }{
		"EventSubscriber:ts": {"apps/web/consumer.ts", "typescript"},
		"EventSubscriber:py": {"pipelines/audit/consumer.py", "python"},
	}
	for id, want := range cases {
		got, err := store.LookupEntityByID(ctx, id)
		if err != nil || got == nil {
			t.Fatalf("lookup %s: %v", id, err)
		}
		if got.Path != want.path {
			t.Errorf("%s Path: got %q, want %q", id, got.Path, want.path)
		}
		if got.LanguageID != want.lang {
			t.Errorf("%s LanguageID: got %q, want %q", id, got.LanguageID, want.lang)
		}
	}
}

// TestEntityWriter_RouteCompositeQN checks the Route projection uses
// the "<METHOD> <path>" composite qualified name the pipeline links on.
func TestEntityWriter_RouteCompositeQN(t *testing.T) {
	store := newWriterTestStore(t)
	ctx := context.Background()
	if err := store.PutEntity(ctx, code_core.Entity{
		ID: "fn:api.GetUser", Kind: code_core.KindFunction, QualifiedName: "api.GetUser", Path: "internal/api/routes.go",
	}, 1); err != nil {
		t.Fatalf("seed handler: %v", err)
	}
	w := NewEntityWriter(store)
	route := Route{
		ID: "Route:abc", Kind: KindRoute, Method: "GET", PathPattern: "/v1/users/{id}", Framework: "chi",
		AnchoredTo: SelectorRef{Anchors: []Anchor{{Kind: "qualified_name", Value: "api.GetUser"}}},
	}
	if err := w.WriteFromEvent(ctx, emit(t, "RouteAdded", "extractor:framework:routes.go.chi", route)); err != nil {
		t.Fatalf("write route: %v", err)
	}
	got, err := store.LookupEntityByID(ctx, "Route:abc")
	if err != nil || got == nil {
		t.Fatalf("lookup route: %v", err)
	}
	if got.QualifiedName != "GET /v1/users/{id}" {
		t.Errorf("Route QN: got %q, want %q", got.QualifiedName, "GET /v1/users/{id}")
	}
	if got.KindTag != "GET" {
		t.Errorf("Route KindTag: got %q, want GET", got.KindTag)
	}
}

// TestEntityWriter_RemovalDeletesRow checks a "*Removed" event drops
// the projected row + its reverse-index bindings.
func TestEntityWriter_RemovalDeletesRow(t *testing.T) {
	store := newWriterTestStore(t)
	ctx := context.Background()
	w := NewEntityWriter(store)
	ev := Event{ID: "Event:abc", Kind: KindEvent, Name: "order.created", Transport: "kafka"}
	if err := w.WriteFromEvent(ctx, emit(t, "EventTopicObserved", "extractor:framework:events.kafka.go", ev)); err != nil {
		t.Fatalf("write event: %v", err)
	}
	if got, _ := store.LookupEntityByID(ctx, "Event:abc"); got == nil {
		t.Fatal("event not materialized before removal")
	}
	if err := w.WriteFromEvent(ctx, emit(t, "EventTopicRemoved", "extractor:framework:events.kafka.go", ev)); err != nil {
		t.Fatalf("write removal: %v", err)
	}
	if got, _ := store.LookupEntityByID(ctx, "Event:abc"); got != nil {
		t.Error("event row should be deleted after *Removed event")
	}
}
