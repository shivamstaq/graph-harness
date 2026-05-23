package code_core

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // SQLite driver
)

// newIndexTestStore opens an empty file-backed SQLite store for the test.
// File-backed (not :memory:) so PRAGMA tuning matches production.
func newIndexTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "code.core.db") +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := NewStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

func TestEntitySelectorIndex_BindAndLookup(t *testing.T) {
	ctx := context.Background()
	s := newIndexTestStore(t)

	if err := s.BindSelector(ctx, "ent-1", "sel-A", "flow-X", "qualified_name", 100); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := s.BindSelector(ctx, "ent-1", "sel-B", "", "route_pattern", 101); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := s.BindSelector(ctx, "ent-2", "sel-A", "flow-X", "qualified_name", 102); err != nil {
		t.Fatalf("bind: %v", err)
	}

	got, err := s.SelectorsBoundTo(ctx, "ent-1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ent-1: want 2 bindings, got %d (%+v)", len(got), got)
	}
	// ORDER BY selector_id → sel-A, sel-B.
	if got[0].SelectorID != "sel-A" || got[0].FlowID != "flow-X" ||
		got[0].ViaAnchor != "qualified_name" || got[0].BoundAtSeq != 100 {
		t.Errorf("ent-1[0] = %+v", got[0])
	}
	if got[1].SelectorID != "sel-B" || got[1].FlowID != "" ||
		got[1].ViaAnchor != "route_pattern" || got[1].BoundAtSeq != 101 {
		t.Errorf("ent-1[1] = %+v", got[1])
	}

	ents, err := s.EntitiesForSelector(ctx, "sel-A")
	if err != nil {
		t.Fatalf("entities-for-selector: %v", err)
	}
	if len(ents) != 2 || ents[0] != "ent-1" || ents[1] != "ent-2" {
		t.Errorf("EntitiesForSelector(sel-A) = %+v, want [ent-1 ent-2]", ents)
	}
}

func TestEntitySelectorIndex_BindIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newIndexTestStore(t)

	// Bind twice at different seqs / anchors → row should be updated,
	// not duplicated.
	if err := s.BindSelector(ctx, "ent", "sel", "flow", "qualified_name", 10); err != nil {
		t.Fatal(err)
	}
	if err := s.BindSelector(ctx, "ent", "sel", "flow", "ast_hash", 20); err != nil {
		t.Fatal(err)
	}
	got, err := s.SelectorsBoundTo(ctx, "ent")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 row after re-bind, got %d", len(got))
	}
	if got[0].ViaAnchor != "ast_hash" || got[0].BoundAtSeq != 20 {
		t.Errorf("re-bind did not refresh row: %+v", got[0])
	}
}

func TestEntitySelectorIndex_UnbindByEntity(t *testing.T) {
	ctx := context.Background()
	s := newIndexTestStore(t)

	_ = s.BindSelector(ctx, "ent-1", "sel-A", "", "qualified_name", 1)
	_ = s.BindSelector(ctx, "ent-1", "sel-B", "", "qualified_name", 2)
	_ = s.BindSelector(ctx, "ent-2", "sel-A", "", "qualified_name", 3)

	// Simulates EntitySuperseded for ent-1.
	if err := s.UnbindSelectorsForEntity(ctx, "ent-1"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.SelectorsBoundTo(ctx, "ent-1")
	if len(got) != 0 {
		t.Errorf("expected ent-1 bindings cleared, got %+v", got)
	}
	got2, _ := s.SelectorsBoundTo(ctx, "ent-2")
	if len(got2) != 1 {
		t.Errorf("ent-2 should be untouched, got %+v", got2)
	}
}

func TestEntitySelectorIndex_UnbindBySelector(t *testing.T) {
	ctx := context.Background()
	s := newIndexTestStore(t)

	_ = s.BindSelector(ctx, "ent-1", "sel-A", "", "qualified_name", 1)
	_ = s.BindSelector(ctx, "ent-2", "sel-A", "", "qualified_name", 2)
	_ = s.BindSelector(ctx, "ent-1", "sel-B", "", "qualified_name", 3)

	if err := s.UnbindSelector(ctx, "sel-A"); err != nil {
		t.Fatal(err)
	}
	ents, _ := s.EntitiesForSelector(ctx, "sel-A")
	if len(ents) != 0 {
		t.Errorf("expected sel-A unbound, got %+v", ents)
	}
	// sel-B still present.
	gotEnt1, _ := s.SelectorsBoundTo(ctx, "ent-1")
	if len(gotEnt1) != 1 || gotEnt1[0].SelectorID != "sel-B" {
		t.Errorf("expected ent-1 to retain sel-B, got %+v", gotEnt1)
	}
}

// TestEntitySelectorIndex_RebindAfterUnbind covers the bind → unbind →
// re-bind cycle to guarantee idempotency under the drift workflow.
func TestEntitySelectorIndex_RebindAfterUnbind(t *testing.T) {
	ctx := context.Background()
	s := newIndexTestStore(t)

	_ = s.BindSelector(ctx, "ent", "sel", "", "qualified_name", 1)
	_ = s.UnbindSelectorsForEntity(ctx, "ent")
	if err := s.BindSelector(ctx, "ent", "sel", "", "qualified_name", 2); err != nil {
		t.Fatal(err)
	}
	got, _ := s.SelectorsBoundTo(ctx, "ent")
	if len(got) != 1 || got[0].BoundAtSeq != 2 {
		t.Errorf("re-bind after unbind = %+v", got)
	}
}

// TestEntitySelectorIndex_LookupIsO1At10K asserts the SQLite PK probe
// on `entity_selector_index(entity_id, ...)` stays fast as the table
// grows. We seed 10K rows then time 1K lookups — each lookup must
// complete in under 10ms (the P2.M03 gate).
func TestEntitySelectorIndex_LookupIsO1At10K(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 10K-row perf test in -short mode")
	}
	ctx := context.Background()
	s := newIndexTestStore(t)

	// Seed 10K (entity, selector) bindings, ~10 selectors per entity
	// for an average of 10 rows per entity_id PK prefix scan.
	const numEntities = 1000
	const selsPerEntity = 10
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for e := 0; e < numEntities; e++ {
		entID := fmt.Sprintf("ent-%05d", e)
		for k := 0; k < selsPerEntity; k++ {
			selID := fmt.Sprintf("sel-%05d-%d", e, k)
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO entity_selector_index (entity_id, selector_id, flow_id, bound_at_seq, via_anchor)
				VALUES (?, ?, NULL, ?, 'qualified_name')`, entID, selID, e*1000+k); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Time 1K random lookups against fixed entity IDs. Each lookup
	// must finish in <10ms (the gate); we also assert the aggregate
	// average is comfortably below that for headroom.
	const numLookups = 1000
	var slowest time.Duration
	for i := 0; i < numLookups; i++ {
		entID := fmt.Sprintf("ent-%05d", i%numEntities)
		start := time.Now()
		got, err := s.SelectorsBoundTo(ctx, entID)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != selsPerEntity {
			t.Fatalf("lookup %s: got %d rows, want %d", entID, len(got), selsPerEntity)
		}
		if d := time.Since(start); d > slowest {
			slowest = d
		}
	}
	if slowest > 10*time.Millisecond {
		t.Errorf("slowest lookup = %v, want <10ms (P2.M03 gate)", slowest)
	}
	t.Logf("10K rows, %d lookups, slowest=%v", numLookups, slowest)
}

func TestEntitySelectorIndex_EmptyLookupReturnsNilNoError(t *testing.T) {
	ctx := context.Background()
	s := newIndexTestStore(t)
	got, err := s.SelectorsBoundTo(ctx, "no-such-entity")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %+v", got)
	}
	ents, err := s.EntitiesForSelector(ctx, "no-such-selector")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(ents) != 0 {
		t.Errorf("expected empty, got %+v", ents)
	}
}
