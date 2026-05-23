package semantic_overlay

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // SQLite driver

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/dsl"
)

// newResolverFixture stands up an in-memory code.core store seeded with
// one function entity and an overlay holding one selector that targets
// it by qualified_name. Returned to the caller for assertion.
func newResolverFixture(t *testing.T) (*code_core.Store, *Overlay, string, context.Context) {
	t.Helper()
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "code.core.db") +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := code_core.NewStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	// Seed one Function entity.
	ent := code_core.Entity{
		ID:            code_core.FunctionID("go", "pkg.Target", "() error"),
		Kind:          code_core.KindFunction,
		LanguageID:    "go",
		QualifiedName: "pkg.Target",
	}
	if err := store.PutEntity(ctx, ent, 1); err != nil {
		t.Fatalf("seed entity: %v", err)
	}

	// Build an overlay programmatically (parser already covered in dsl tests).
	str := "pkg.Target"
	sel := &dsl.Selector{
		Name: "SelTarget",
		Anchors: []*dsl.Anchor{
			{Kind: "qualified_name", Value: &dsl.AnchorValue{Str: &str}},
		},
	}
	o := NewOverlay()
	o.Selectors[sel.Name] = sel
	return store, o, ent.ID, ctx
}

// TestResolve_BindsReverseIndex asserts the resolver pipes successful
// matches into entity_selector_index so a subsequent SelectorsBoundTo
// query returns the selector. This is the Pass-0.5-B integration gate.
func TestResolve_BindsReverseIndex(t *testing.T) {
	store, o, entID, ctx := newResolverFixture(t)

	env, err := o.Resolve(ctx, "SelTarget", store, 42)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if env.Outcome != OutcomeBound {
		t.Fatalf("expected bound outcome, got %s", env.Outcome)
	}
	got, err := store.SelectorsBoundTo(ctx, entID)
	if err != nil {
		t.Fatalf("reverse lookup: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 binding for %s, got %d (%+v)", entID, len(got), got)
	}
	if got[0].SelectorID != "SelTarget" {
		t.Errorf("binding selector_id = %q, want SelTarget", got[0].SelectorID)
	}
	if got[0].ViaAnchor != "qualified_name" {
		t.Errorf("binding via_anchor = %q, want qualified_name", got[0].ViaAnchor)
	}
	if got[0].BoundAtSeq != 42 {
		t.Errorf("binding bound_at_seq = %d, want 42", got[0].BoundAtSeq)
	}
}

// TestResolve_UnresolvedDoesNotBind asserts the resolver does NOT
// write a reverse-index row when the selector's anchor matches no
// entity (Outcome = unresolved).
func TestResolve_UnresolvedDoesNotBind(t *testing.T) {
	store, o, _, ctx := newResolverFixture(t)

	// Add an unresolvable selector.
	miss := "pkg.DoesNotExist"
	o.Selectors["Missing"] = &dsl.Selector{
		Name: "Missing",
		Anchors: []*dsl.Anchor{
			{Kind: "qualified_name", Value: &dsl.AnchorValue{Str: &miss}},
		},
	}

	env, err := o.Resolve(ctx, "Missing", store, 7)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if env.Outcome != OutcomeUnresolved {
		t.Fatalf("expected unresolved, got %s", env.Outcome)
	}
	// No entity → EntitiesForSelector returns empty.
	ents, _ := store.EntitiesForSelector(ctx, "Missing")
	if len(ents) != 0 {
		t.Errorf("unresolved should not bind, got %+v", ents)
	}
}

// TestResolve_DriftSupersedesBindings simulates an EntitySuperseded
// event by directly invoking UnbindSelectorsForEntity on the resolver's
// store after a successful bind. Asserts the reverse-index row is gone.
func TestResolve_DriftSupersedesBindings(t *testing.T) {
	store, o, entID, ctx := newResolverFixture(t)

	if _, err := o.Resolve(ctx, "SelTarget", store, 42); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	pre, _ := store.SelectorsBoundTo(ctx, entID)
	if len(pre) != 1 {
		t.Fatalf("setup: expected 1 binding, got %d", len(pre))
	}

	// Simulate EntitySuperseded.
	if err := store.UnbindSelectorsForEntity(ctx, entID); err != nil {
		t.Fatalf("unbind: %v", err)
	}
	post, _ := store.SelectorsBoundTo(ctx, entID)
	if len(post) != 0 {
		t.Errorf("expected bindings cleared after EntitySuperseded, got %+v", post)
	}
}

// TestResolve_RebindIsIdempotent asserts re-running Resolve on the same
// selector updates rather than duplicates the reverse-index row.
func TestResolve_RebindIsIdempotent(t *testing.T) {
	store, o, entID, ctx := newResolverFixture(t)

	if _, err := o.Resolve(ctx, "SelTarget", store, 1); err != nil {
		t.Fatalf("resolve 1: %v", err)
	}
	if _, err := o.Resolve(ctx, "SelTarget", store, 2); err != nil {
		t.Fatalf("resolve 2: %v", err)
	}
	if _, err := o.Resolve(ctx, "SelTarget", store, 3); err != nil {
		t.Fatalf("resolve 3: %v", err)
	}
	got, _ := store.SelectorsBoundTo(ctx, entID)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 binding after 3 resolves, got %d", len(got))
	}
	if got[0].BoundAtSeq != 3 {
		t.Errorf("bound_at_seq = %d, want 3 (latest resolve seq)", got[0].BoundAtSeq)
	}
}
