package jsonrpc

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/semantic_overlay"
)

// TestFrameworkBrowser_ListsByKind seeds the code.core store with one
// Route, one EventPublisher, and one Schema entity (matching the
// Pass-0.5-A convention: framework entities live in code_entities
// with their framework Kind discriminator) and asserts the
// framework.* handlers surface them with stable Kind filtering.
func TestFrameworkBrowser_ListsByKind(t *testing.T) {
	t.Parallel()
	svc := newFrameworkBrowserService(t)
	ctx := context.Background()

	seedFrameworkEntities(t, ctx, svc.Code)

	t.Run("routes", func(t *testing.T) {
		r, err := svc.FrameworkRoutes(ctx, FrameworkListParams{})
		if err != nil {
			t.Fatalf("FrameworkRoutes: %v", err)
		}
		if len(r.Items) != 1 {
			t.Fatalf("want 1 route, got %d (%+v)", len(r.Items), r.Items)
		}
		if got := r.Items[0].QualifiedName; got != "/v1/orders/:id" {
			t.Errorf("route qn: want /v1/orders/:id, got %q", got)
		}
		if got := r.Items[0].KindTag; got != "GET" {
			t.Errorf("route kind_tag: want GET, got %q", got)
		}
		if r.Total != 1 {
			t.Errorf("Total: want 1, got %d", r.Total)
		}
	})

	t.Run("event_publishers", func(t *testing.T) {
		r, err := svc.FrameworkEventPublishers(ctx, FrameworkListParams{})
		if err != nil {
			t.Fatalf("FrameworkEventPublishers: %v", err)
		}
		if len(r.Items) != 1 {
			t.Fatalf("want 1 publisher, got %d", len(r.Items))
		}
		if got := r.Items[0].QualifiedName; got != "order.placed" {
			t.Errorf("publisher qn: want order.placed, got %q", got)
		}
	})

	t.Run("schemas", func(t *testing.T) {
		r, err := svc.FrameworkSchemas(ctx, FrameworkListParams{})
		if err != nil {
			t.Fatalf("FrameworkSchemas: %v", err)
		}
		if len(r.Items) != 1 {
			t.Fatalf("want 1 schema, got %d", len(r.Items))
		}
		if got := r.Items[0].QualifiedName; got != "orders" {
			t.Errorf("schema table: want orders, got %q", got)
		}
		if got := r.Items[0].KindTag; got != "prisma" {
			t.Errorf("schema dialect: want prisma, got %q", got)
		}
	})
}

// TestFrameworkBrowser_FrameworkFilter exercises the per-handler
// filter helpers: route framework substring vs event transport vs
// schema dialect exact-match vs schema-field table-prefix.
func TestFrameworkBrowser_FrameworkFilter(t *testing.T) {
	t.Parallel()
	svc := newFrameworkBrowserService(t)
	ctx := context.Background()
	store := svc.Code

	// Two routes — one chi, one express. The framework discriminator
	// lives on KindTag per filterByFramework's substring rule (Pass-1
	// route extractors stamp method,framework into the kind_tag slot).
	mustPut(t, ctx, store, code_core.Entity{
		ID:            "route-chi-1",
		Kind:          "Route",
		QualifiedName: "/v1/orders/:id",
		KindTag:       "GET,chi",
	})
	mustPut(t, ctx, store, code_core.Entity{
		ID:            "route-express-1",
		Kind:          "Route",
		QualifiedName: "/users/:id",
		KindTag:       "GET,express",
	})
	// Two events — kafka and nats topics.
	mustPut(t, ctx, store, code_core.Entity{
		ID:            "ev-kafka-1",
		Kind:          "Event",
		QualifiedName: "order.placed",
		KindTag:       "topic:kafka",
	})
	mustPut(t, ctx, store, code_core.Entity{
		ID:            "ev-nats-1",
		Kind:          "Event",
		QualifiedName: "user.created",
		KindTag:       "topic:nats",
	})
	// Schemas — prisma + drizzle.
	mustPut(t, ctx, store, code_core.Entity{
		ID:            "schema-prisma-1",
		Kind:          "Schema",
		QualifiedName: "orders",
		KindTag:       "prisma",
	})
	mustPut(t, ctx, store, code_core.Entity{
		ID:            "schema-drizzle-1",
		Kind:          "Schema",
		QualifiedName: "users",
		KindTag:       "drizzle",
	})
	// SchemaFields for the orders table.
	mustPut(t, ctx, store, code_core.Entity{
		ID:            "field-orders-id",
		Kind:          "SchemaField",
		QualifiedName: "orders.id",
	})
	mustPut(t, ctx, store, code_core.Entity{
		ID:            "field-users-email",
		Kind:          "SchemaField",
		QualifiedName: "users.email",
	})

	t.Run("route framework filter (chi)", func(t *testing.T) {
		r, err := svc.FrameworkRoutes(ctx, FrameworkListParams{Framework: "chi"})
		if err != nil {
			t.Fatalf("FrameworkRoutes: %v", err)
		}
		if len(r.Items) != 1 || r.Items[0].ID != "route-chi-1" {
			t.Errorf("framework=chi: want [route-chi-1], got %+v", r.Items)
		}
	})

	t.Run("event transport filter (kafka)", func(t *testing.T) {
		r, err := svc.FrameworkEvents(ctx, FrameworkListParams{Framework: "kafka"})
		if err != nil {
			t.Fatalf("FrameworkEvents: %v", err)
		}
		if len(r.Items) != 1 || r.Items[0].ID != "ev-kafka-1" {
			t.Errorf("framework=kafka: want [ev-kafka-1], got %+v", r.Items)
		}
	})

	t.Run("schema dialect filter (drizzle)", func(t *testing.T) {
		r, err := svc.FrameworkSchemas(ctx, FrameworkListParams{Framework: "drizzle"})
		if err != nil {
			t.Fatalf("FrameworkSchemas: %v", err)
		}
		if len(r.Items) != 1 || r.Items[0].ID != "schema-drizzle-1" {
			t.Errorf("framework=drizzle: want [schema-drizzle-1], got %+v", r.Items)
		}
	})

	t.Run("schema_fields table prefix (orders)", func(t *testing.T) {
		r, err := svc.FrameworkSchemaFields(ctx, FrameworkListParams{Framework: "orders"})
		if err != nil {
			t.Fatalf("FrameworkSchemaFields: %v", err)
		}
		if len(r.Items) != 1 || r.Items[0].ID != "field-orders-id" {
			t.Errorf("framework=orders: want [field-orders-id], got %+v", r.Items)
		}
	})

	t.Run("no filter returns all per kind", func(t *testing.T) {
		r, err := svc.FrameworkEvents(ctx, FrameworkListParams{})
		if err != nil {
			t.Fatalf("FrameworkEvents: %v", err)
		}
		if len(r.Items) != 2 {
			t.Errorf("no filter: want 2 events, got %d", len(r.Items))
		}
	})
}

// TestFrameworkBrowser_StepsTouching seeds a Route entity plus a
// selector→entity binding in the reverse selector index and asserts
// FrameworkStepsTouching surfaces the binding row. This exercises the
// Pass-0.5-A reverse index that backs Studio's "show flow steps that
// touch this route" drill-down.
func TestFrameworkBrowser_StepsTouching(t *testing.T) {
	t.Parallel()
	svc := newFrameworkBrowserService(t)
	ctx := context.Background()
	store := svc.Code

	mustPut(t, ctx, store, code_core.Entity{
		ID:            "route-bound-1",
		Kind:          "Route",
		QualifiedName: "/v1/orders/:id",
		KindTag:       "GET",
	})
	if err := store.BindSelector(ctx, "route-bound-1", "sel:OrderRoute", "flow:Checkout", "route_pattern", 42); err != nil {
		t.Fatalf("BindSelector: %v", err)
	}

	t.Run("by entity_id", func(t *testing.T) {
		r, err := svc.FrameworkStepsTouching(ctx, FrameworkStepsTouchingParams{EntityID: "route-bound-1"})
		if err != nil {
			t.Fatalf("FrameworkStepsTouching: %v", err)
		}
		if r.EntityID != "route-bound-1" {
			t.Errorf("entity_id echo: want route-bound-1, got %q", r.EntityID)
		}
		if len(r.Steps) != 1 {
			t.Fatalf("want 1 step, got %d", len(r.Steps))
		}
		s := r.Steps[0]
		if s.SelectorID != "sel:OrderRoute" {
			t.Errorf("selector_id: want sel:OrderRoute, got %q", s.SelectorID)
		}
		if s.FlowID != "flow:Checkout" {
			t.Errorf("flow_id: want flow:Checkout, got %q", s.FlowID)
		}
		if s.ViaAnchor != "route_pattern" {
			t.Errorf("via_anchor: want route_pattern, got %q", s.ViaAnchor)
		}
		if s.BoundAtSeq != 42 {
			t.Errorf("bound_at_seq: want 42, got %d", s.BoundAtSeq)
		}
	})

	t.Run("by qualified_name", func(t *testing.T) {
		r, err := svc.FrameworkStepsTouching(ctx, FrameworkStepsTouchingParams{QualifiedName: "/v1/orders/:id"})
		if err != nil {
			t.Fatalf("FrameworkStepsTouching by QN: %v", err)
		}
		if len(r.Steps) != 1 {
			t.Errorf("by QN: want 1 step, got %d", len(r.Steps))
		}
	})

	t.Run("unknown entity returns empty", func(t *testing.T) {
		r, err := svc.FrameworkStepsTouching(ctx, FrameworkStepsTouchingParams{EntityID: "does-not-exist"})
		if err != nil {
			t.Fatalf("unknown: %v", err)
		}
		if len(r.Steps) != 0 {
			t.Errorf("unknown entity: want 0 steps, got %d", len(r.Steps))
		}
		// Steps is initialized to []FrameworkStepsTouchingRow{} so JSON
		// serializes to [] rather than null.
		if r.Steps == nil {
			t.Errorf("Steps must be non-nil for stable JSON shape")
		}
	})
}

// --- test helpers ---------------------------------------------------------

// newFrameworkBrowserService builds an isolated Service with a fresh
// SQLite-backed code.core store and the bare minimum collaborators
// the framework.* handlers exercise (overlay + event log are required
// for the constructor signature but not invoked by these tests).
func newFrameworkBrowserService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kernel.db")
	log, err := facts.OpenEventLog(logPath)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	ws := &daemon.Workspace{
		Root:     dir,
		StateDir: filepath.Join(dir, ".graph-harness"),
		EventLog: logPath,
		ID:       "test",
	}
	store := newEmptyCodeStore(t, filepath.Join(dir, "code.db"))
	queue := newEmptyQueue(t, filepath.Join(dir, "queue.db"))
	overlay := semantic_overlay.NewOverlay()
	reg := kernel.NewRegistry()
	return NewService(ws, log, store, queue, reg, overlay)
}

// seedFrameworkEntities loads one canonical entity per Route /
// EventPublisher / Schema kind so the lists-by-kind test has stable
// minimal fixture data.
func seedFrameworkEntities(t *testing.T, ctx context.Context, store *code_core.Store) {
	t.Helper()
	mustPut(t, ctx, store, code_core.Entity{
		ID:            "route-1",
		Kind:          "Route",
		QualifiedName: "/v1/orders/:id",
		KindTag:       "GET",
	})
	mustPut(t, ctx, store, code_core.Entity{
		ID:            "pub-1",
		Kind:          "EventPublisher",
		QualifiedName: "order.placed",
		KindTag:       "kafka",
	})
	mustPut(t, ctx, store, code_core.Entity{
		ID:            "schema-1",
		Kind:          "Schema",
		QualifiedName: "orders",
		KindTag:       "prisma",
	})
}

func mustPut(t *testing.T, ctx context.Context, store *code_core.Store, e code_core.Entity) {
	t.Helper()
	if err := store.PutEntity(ctx, e, 1); err != nil {
		t.Fatalf("PutEntity %s: %v", e.ID, err)
	}
}
