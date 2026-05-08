package code_core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// captureEmitter records every SymbolDisambiguation event for assertion.
type captureEmitter struct {
	mu     sync.Mutex
	events []capturedEvent
}

type capturedEvent struct {
	kind    string
	payload SymbolDisambiguationPayload
}

func (c *captureEmitter) EmitCodeCoreEvent(_ context.Context, kind string, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var p SymbolDisambiguationPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return err
	}
	c.events = append(c.events, capturedEvent{kind: kind, payload: p})
	return nil
}

func (c *captureEmitter) all() []capturedEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]capturedEvent, len(c.events))
	copy(out, c.events)
	return out
}

// newTestStore opens an in-memory SQLite store. Each call gets its
// own database, so tests are independent.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?cache=shared&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// fakeSymbol fabricates a Symbol envelope as if emitted by the named
// source. Each fact source is expected to produce identical Symbol
// fields when they observe the same source-text entity — the test
// helpers therefore parameterize only the source class + producer
// label, not the entity-identity inputs.
func fakeSymbol(source source_live.SourceClass, producer string, fn fakeFn) source_live.Symbol {
	return source_live.Symbol{
		Name:          fn.name,
		QualifiedName: fn.qn,
		Kind:          source_live.SymbolKindFunction,
		Range: source_live.Range{
			StartByte: fn.startByte,
			EndByte:   fn.endByte,
			StartLine: 1,
			EndLine:   1,
		},
		Signature:   fn.signature,
		LanguageID:  fn.languageID,
		Path:        fn.path,
		BodyHash:    fn.bodyHash,
		SourceClass: source,
		ProducedBy:  producer,
		Confidence:  fn.confidence,
	}
}

type fakeFn struct {
	name       string
	qn         string
	signature  string
	languageID string
	path       string
	bodyHash   string
	startByte  uint32
	endByte    uint32
	confidence float64
}

func (f fakeFn) emitAll() []source_live.Symbol {
	return []source_live.Symbol{
		fakeSymbol(source_live.SourceClassLSP, "extractor:lsp:fake", f),
		fakeSymbol(source_live.SourceClassSCIP, "extractor:scip:fake", f),
		fakeSymbol(source_live.SourceClassTreesitter, "extractor:treesitter:fake", f),
	}
}

// genFn produces a random fake-function fixture. The languages cycle
// through go/typescript/python so the property tests cover all three.
func genFn(rng *rand.Rand, i int) fakeFn {
	langs := []string{"go", "typescript", "python"}
	lang := langs[i%len(langs)]
	startByte := uint32(rng.IntN(10_000_000))
	bodyLen := uint32(rng.IntN(1000) + 10)
	signatures := map[string][]string{
		"go":         {"(a int, b string) error", "[T any](x T) T", "(ctx context.Context) error"},
		"typescript": {"(req: Request, res: Response): Promise<void>", "<T>(x: T): T"},
		"python":     {"(self, value: int) -> bool", "(self, *, k: int = 1, m: int = 2) -> None"},
	}
	sigs := signatures[lang]
	return fakeFn{
		name:       fmt.Sprintf("Fn%d", i),
		qn:         fmt.Sprintf("pkg%d.Fn%d", i%5, i),
		signature:  sigs[rng.IntN(len(sigs))],
		languageID: lang,
		path:       fmt.Sprintf("pkg%d/file%d.%s", i%5, i, langExt(lang)),
		bodyHash:   fmt.Sprintf("hash-%d-%d-%d", i, startByte, bodyLen),
		startByte:  startByte,
		endByte:    startByte + bodyLen,
		confidence: 0.9 + rng.Float64()*0.1,
	}
}

func langExt(lang string) string {
	switch lang {
	case "go":
		return "go"
	case "typescript":
		return "ts"
	case "python":
		return "py"
	}
	return "txt"
}

// TestThreeSourceAgreement is gate criterion 4 (plan §3): 50 random
// fixtures where all three sources independently observe the same
// function. The unifier must produce exactly one entity per fixture
// and three provenance entries (one per source).
func TestThreeSourceAgreement(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(0x6c, 0x6f)) //nolint:gosec // deterministic test seed
	store := newTestStore(t)
	emitter := &captureEmitter{}
	u := &Unifier{Store: store, Emitter: emitter}
	ctx := context.Background()

	const n = 50
	fixtures := make([]fakeFn, n)
	var allSyms []source_live.Symbol
	for i := 0; i < n; i++ {
		fixtures[i] = genFn(rng, i)
		allSyms = append(allSyms, fixtures[i].emitAll()...)
	}
	written, err := u.Unify(ctx, allSyms, 100)
	if err != nil {
		t.Fatalf("Unify: %v", err)
	}
	if got := dedupCount(written); got != n {
		t.Fatalf("expected %d distinct entities written; got %d (raw=%d)", n, got, len(written))
	}
	if events := emitter.all(); len(events) != 0 {
		t.Errorf("agreement scenario should not emit SymbolDisambiguation; got %d events", len(events))
	}

	// Spot-check provenance: each entity must carry all three sources.
	for _, f := range fixtures {
		ent, _ := entityFromSymbol(fakeSymbol(source_live.SourceClassLSP, "x", f))
		entries, err := store.GetProvenance(ctx, ent.ID)
		if err != nil {
			t.Fatalf("GetProvenance(%s): %v", ent.ID, err)
		}
		if len(entries) != 3 {
			t.Errorf("entity %s should have 3 provenance entries; got %d", ent.ID, len(entries))
		}
		seen := make(map[SourceClass]bool, 3)
		for _, e := range entries {
			seen[e.SourceClass] = true
		}
		for _, sc := range []SourceClass{SourceClassLSP, SourceClassSCIP, SourceClassTreesitter} {
			if !seen[sc] {
				t.Errorf("entity %s missing provenance for %s", ent.ID, sc)
			}
		}
	}
}

// TestConflictAsEvent is gate criterion 5 (plan §3): 20 disagreement
// fixtures. Each fixture contains three Symbols at the same source
// location whose canonical IDs differ (one source disagrees on the
// signature). The unifier must materialize all distinct IDs as
// separate entities AND emit one SymbolDisambiguation event per
// fixture with all source claims.
func TestConflictAsEvent(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(0x42, 0x42)) //nolint:gosec // deterministic test seed
	store := newTestStore(t)
	emitter := &captureEmitter{}
	u := &Unifier{Store: store, Emitter: emitter}
	ctx := context.Background()

	const n = 20
	var allSyms []source_live.Symbol
	for i := 0; i < n; i++ {
		f := genFn(rng, i)
		// LSP/treesitter agree, SCIP disagrees on signature.
		lsp := fakeSymbol(source_live.SourceClassLSP, "extractor:lsp:fake", f)
		ts := fakeSymbol(source_live.SourceClassTreesitter, "extractor:treesitter:fake", f)
		fScip := f
		fScip.signature += " /* disagrees */ "
		scip := fakeSymbol(source_live.SourceClassSCIP, "extractor:scip:fake", fScip)
		allSyms = append(allSyms, lsp, scip, ts)
	}
	written, err := u.Unify(ctx, allSyms, 200)
	if err != nil {
		t.Fatalf("Unify: %v", err)
	}
	if got := dedupCount(written); got != 2*n {
		t.Errorf("disagreement should yield 2 entities per fixture; got %d (want %d)", got, 2*n)
	}
	events := emitter.all()
	if len(events) != n {
		t.Fatalf("expected %d SymbolDisambiguation events; got %d", n, len(events))
	}
	for i, ev := range events {
		if ev.kind != "SymbolDisambiguation" {
			t.Errorf("event %d kind = %q; want SymbolDisambiguation", i, ev.kind)
		}
		if got := len(ev.payload.Claims); got != 2 {
			t.Errorf("event %d claims = %d; want 2", i, got)
		}
		// The combined source-class set across all claims must cover
		// all three sources, demonstrating "every source's claim is
		// recorded."
		seen := map[SourceClass]bool{}
		for _, c := range ev.payload.Claims {
			for _, s := range c.Sources {
				seen[s.SourceClass] = true
			}
		}
		for _, sc := range []SourceClass{SourceClassLSP, SourceClassSCIP, SourceClassTreesitter} {
			if !seen[sc] {
				t.Errorf("event %d missing claim from source %s", i, sc)
			}
		}
		if ev.payload.Threshold == 0 {
			t.Errorf("event %d should record disagreement threshold", i)
		}
	}
}

func TestUnifier_DisagreementThresholdGatesEmission(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	emitter := &captureEmitter{}
	// Threshold raised to 3 — a 2-way disagreement should NOT emit.
	u := &Unifier{Store: store, Emitter: emitter, DisagreementThreshold: 3}
	ctx := context.Background()
	f := genFn(rand.New(rand.NewPCG(1, 1)), 0) //nolint:gosec
	lsp := fakeSymbol(source_live.SourceClassLSP, "x", f)
	fOther := f
	fOther.signature += " /* x */ "
	scip := fakeSymbol(source_live.SourceClassSCIP, "x", fOther)
	if _, err := u.Unify(ctx, []source_live.Symbol{lsp, scip}, 1); err != nil {
		t.Fatalf("Unify: %v", err)
	}
	if events := emitter.all(); len(events) != 0 {
		t.Errorf("threshold=3 should suppress 2-way disagreement; got %d events", len(events))
	}
}

func TestUnifier_IdempotentReingest(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	emitter := &captureEmitter{}
	u := &Unifier{Store: store, Emitter: emitter}
	ctx := context.Background()
	f := genFn(rand.New(rand.NewPCG(2, 2)), 7) //nolint:gosec
	syms := f.emitAll()
	if _, err := u.Unify(ctx, syms, 1); err != nil {
		t.Fatalf("Unify 1: %v", err)
	}
	if _, err := u.Unify(ctx, syms, 2); err != nil {
		t.Fatalf("Unify 2: %v", err)
	}
	ent, _ := entityFromSymbol(syms[0])
	entries, err := store.GetProvenance(ctx, ent.ID)
	if err != nil {
		t.Fatalf("GetProvenance: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("re-ingest should keep one provenance row per source; got %d", len(entries))
	}
	for _, e := range entries {
		if e.LastSeenSeq != 2 {
			t.Errorf("re-ingest should refresh last_seen_seq to 2; got %d for %s", e.LastSeenSeq, e.SourceClass)
		}
	}
}

func TestProvenance_MergePrefersFresher(t *testing.T) {
	t.Parallel()
	var p Provenance
	p.Merge(SourceEntry{SourceClass: SourceClassLSP, Confidence: 0.8, LastSeenSeq: 5})
	p.Merge(SourceEntry{SourceClass: SourceClassLSP, Confidence: 0.6, LastSeenSeq: 10})
	if got := p.Sources[0].LastSeenSeq; got != 10 {
		t.Errorf("Merge should prefer fresher seq; got LastSeenSeq=%d", got)
	}
	if got := p.Sources[0].Confidence; got != 0.6 {
		t.Errorf("Merge should accept fresher entry verbatim; got Confidence=%v", got)
	}
}

func TestProvenance_CanonicalizesOrder(t *testing.T) {
	t.Parallel()
	var p Provenance
	p.Merge(SourceEntry{SourceClass: SourceClassTreesitter, LastSeenSeq: 1})
	p.Merge(SourceEntry{SourceClass: SourceClassLSP, LastSeenSeq: 1})
	p.Merge(SourceEntry{SourceClass: SourceClassSCIP, LastSeenSeq: 1})
	want := []SourceClass{SourceClassSCIP, SourceClassLSP, SourceClassTreesitter}
	got := p.SourceClasses()
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("canonical order index %d = %s; want %s", i, got[i], want[i])
		}
	}
}

func TestAnonymousID_StableUnderEdit(t *testing.T) {
	t.Parallel()
	a := AnonymousID("parent-abc", "Closure", 2)
	b := AnonymousID("parent-abc", "Closure", 2)
	if a != b {
		t.Errorf("identical inputs should yield identical AnonymousID")
	}
	c := AnonymousID("parent-abc", "Closure", 3)
	if a == c {
		t.Errorf("ordinal change should yield different ID")
	}
	d := AnonymousID("parent-abc", "Lambda", 2)
	if a == d {
		t.Errorf("kind_tag change should yield different ID")
	}
}

func TestSymbolID_DistinguishesByKindTag(t *testing.T) {
	t.Parallel()
	a := SymbolID("typescript", "pkg.X", "Enum")
	b := SymbolID("typescript", "pkg.X", "Const")
	if a == b {
		t.Errorf("kind_tag should disambiguate Symbol IDs")
	}
}

func dedupCount(ids []string) int {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		seen[id] = struct{}{}
	}
	return len(seen)
}

// TestUnifier_PersistsNormalizedSignature is the round-trip guard for
// the anchor-evaluator columns added in support of T-dsl-pipeline's
// function_signature evaluator. Storing the canonical signature
// alongside the entity lets the resolver match without re-parsing the
// source file; if PutEntity / Lookup* were to drop the column on
// either side of the round trip the function_signature anchor would
// silently match nothing, so this test is the contract guard.
func TestUnifier_PersistsNormalizedSignature(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	u := &Unifier{Store: store}
	ctx := context.Background()
	f := genFn(rand.New(rand.NewPCG(7, 7)), 0) //nolint:gosec
	if _, err := u.Unify(ctx, f.emitAll(), 1); err != nil {
		t.Fatalf("Unify: %v", err)
	}
	got, err := store.LookupByQualifiedName(ctx, f.qn)
	if err != nil {
		t.Fatalf("LookupByQualifiedName: %v", err)
	}
	if got == nil {
		t.Fatalf("entity %s not found after Unify", f.qn)
	}
	if got.NormalizedSignature == "" {
		t.Errorf("NormalizedSignature should be persisted; got empty for %s", f.qn)
	}
}
