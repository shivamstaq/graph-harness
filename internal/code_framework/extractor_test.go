package code_framework

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"

	_ "modernc.org/sqlite" // SQLite driver for EventLog
)

// ---- Manifest round-trip --------------------------------------------------

// TestExtractorContract is the Pass-0 gate test. It enforces:
//  1. Compile-time Extractor interface satisfaction by a fake.
//  2. Manifest schema round-trip — every EntityKind constant in
//     types.go appears in schema.entity_kinds and every kind in
//     EmittedEventKinds appears in events.emits.
//  3. Lifecycle — Register, NewDispatcher, Enable/Disable/Status
//     drive a fake extractor through an in-memory event log.
//  4. EntityRefCache — singleflight, path-keyed and entity-keyed
//     invalidation.
func TestExtractorContract(t *testing.T) {
	t.Run("interface-satisfied", testInterfaceSatisfied)
	t.Run("manifest-round-trip", testManifestRoundTrip)
	t.Run("lifecycle", testLifecycle)
	t.Run("entityref-cache", testEntityRefCache)
}

// fakeExtractor is the in-test implementation used to prove the
// interface is satisfiable and to exercise the dispatcher lifecycle.
type fakeExtractor struct {
	name   string
	inputs []EventKind
	calls  atomic.Uint64

	mu      sync.Mutex
	lastIn  kernel.Event
	emitFn  func(in kernel.Event) []kernel.Event
	failErr error
}

func (f *fakeExtractor) Name() string          { return f.name }
func (f *fakeExtractor) Inputs() []EventKind   { return f.inputs }
func (f *fakeExtractor) Outputs() []EntityKind { return []EntityKind{KindRoute} }
func (f *fakeExtractor) Capabilities() Capabilities {
	return Capabilities{Family: "test", Languages: []string{"go"}}
}
func (f *fakeExtractor) OnEvent(_ context.Context, in kernel.Event) ([]kernel.Event, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.lastIn = in
	emit := f.emitFn
	err := f.failErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if emit == nil {
		return nil, nil
	}
	return emit(in), nil
}

// compile-time interface assertion — fails to build if the interface
// drifts.
var _ Extractor = (*fakeExtractor)(nil)

func testInterfaceSatisfied(t *testing.T) {
	// The package-level _ = (*Extractor)((*fakeExtractor)(nil)) above
	// is the real gate; this test just documents it exists.
	var x Extractor = &fakeExtractor{name: "test"}
	if x.Name() != "test" {
		t.Fatalf("Name(): got %q, want %q", x.Name(), "test")
	}
}

func testManifestRoundTrip(t *testing.T) {
	// Load the embedded manifest YAML; assert it parses, validates,
	// and every EntityKind / EmittedEventKind constant declared in
	// types.go is present (and vice versa).
	path := filepath.Join("..", "..", "internal", "kernel", "embedded_manifests", "code.framework.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		// Try via the embedded copy if the relative path drifts.
		path = filepath.Join("..", "kernel", "embedded_manifests", "code.framework.yaml")
		data, err = os.ReadFile(path)
		if err != nil {
			t.Fatalf("read manifest: %v", err)
		}
	}
	m, err := kernel.ParseManifest(data)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("validate manifest: %v", err)
	}

	yamlKinds := map[string]struct{}{}
	for _, ek := range m.Schema.EntityKinds {
		yamlKinds[ek.Name] = struct{}{}
	}
	for _, want := range AllEntityKinds {
		if _, ok := yamlKinds[string(want)]; !ok {
			t.Errorf("manifest missing entity_kind %q (declared in types.go AllEntityKinds)", want)
		}
	}
	for declared := range yamlKinds {
		if !containsEK(AllEntityKinds, declared) {
			t.Errorf("manifest has entity_kind %q not in AllEntityKinds — keep them in sync", declared)
		}
	}

	yamlEvents := map[string]struct{}{}
	for _, e := range m.Events.Emits {
		yamlEvents[e] = struct{}{}
	}
	for _, want := range EmittedEventKinds {
		if _, ok := yamlEvents[want]; !ok {
			t.Errorf("manifest missing event kind %q (declared in EmittedEventKinds)", want)
		}
	}
	for declared := range yamlEvents {
		if !contains(EmittedEventKinds, declared) {
			t.Errorf("manifest has event kind %q not in EmittedEventKinds", declared)
		}
	}

	if m.Metadata.Name != "code.framework" {
		t.Errorf("metadata.name: got %q, want %q", m.Metadata.Name, "code.framework")
	}
	if !contains(m.Trust.DirectWritesFrom, "extractor:framework") {
		t.Errorf("trust.direct_writes_from missing %q", "extractor:framework")
	}
}

func testLifecycle(t *testing.T) {
	// Clean slate so blank-imported real extractors (none in Pass 0,
	// but defensively) don't pollute the registry.
	ResetRegistryForTest()
	t.Cleanup(ResetRegistryForTest)

	// Register two fake extractors with overlapping inputs.
	makeCtor := func(f *fakeExtractor) Constructor {
		return func(Deps) (Extractor, error) { return f, nil }
	}
	a := &fakeExtractor{name: "fake.a", inputs: []EventKind{InputCoreFileChanged}}
	b := &fakeExtractor{name: "fake.b", inputs: []EventKind{InputCoreFileChanged}}
	Register("fake.a", makeCtor(a), Descriptor{Name: "fake.a", Family: "test", Inputs: []EventKind{InputCoreFileChanged}, Outputs: []EntityKind{KindRoute}})
	Register("fake.b", makeCtor(b), Descriptor{Name: "fake.b", Family: "test", Inputs: []EventKind{InputCoreFileChanged}, Outputs: []EntityKind{KindRoute}})

	tmp := t.TempDir()
	log, err := facts.OpenEventLog(filepath.Join(tmp, "events.db"))
	if err != nil {
		t.Fatalf("OpenEventLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	d, err := NewDispatcher(DispatcherConfig{
		Workspace: tmp,
		Facts:     newFactsFromLog(log),
		EventLog:  log,
		Logf:      func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = d.Stop(context.Background()) })

	// Disable fake.b; only fake.a should see the event.
	if err := d.Disable(ctx, "fake.b"); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	// Append a FileChanged event.
	payload, _ := json.Marshal(map[string]any{"path": "x.go"})
	_, err = log.Append(ctx, []kernel.Event{{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	}})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Wait briefly for the dispatcher loop to process.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if a.calls.Load() == 1 && b.calls.Load() == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if a.calls.Load() != 1 {
		t.Errorf("fake.a calls: got %d, want 1", a.calls.Load())
	}
	if b.calls.Load() != 0 {
		t.Errorf("fake.b calls: got %d, want 0 (disabled)", b.calls.Load())
	}

	// Re-enable fake.b, append another event; both should see it.
	if err := d.Enable(ctx, "fake.b"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	_, err = log.Append(ctx, []kernel.Event{{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	}})
	if err != nil {
		t.Fatalf("Append 2: %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if a.calls.Load() == 2 && b.calls.Load() == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if a.calls.Load() != 2 {
		t.Errorf("fake.a calls after re-enable: got %d, want 2", a.calls.Load())
	}
	if b.calls.Load() != 1 {
		t.Errorf("fake.b calls after re-enable: got %d, want 1", b.calls.Load())
	}

	// Status snapshot has both extractors.
	st := d.Status()
	if len(st) != 2 {
		t.Fatalf("Status() len: got %d, want 2", len(st))
	}
	for _, s := range st {
		if s.Name != "fake.a" && s.Name != "fake.b" {
			t.Errorf("unexpected status name %q", s.Name)
		}
	}

	// Confirm the per-extractor TOML was persisted across the
	// disable/enable cycle.
	cfg, err := LoadConfig(tmp)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.IsDisabled("fake.b") {
		t.Errorf("fake.b should NOT be disabled in persisted config after Enable")
	}
}

func testEntityRefCache(t *testing.T) {
	fakeResolver := &fakeResolverImpl{
		resolve: func(_ context.Context, sel SelectorRef, _ uint64) (EntityRef, error) {
			return EntityRef{
				Ref:        kernel.EntityRef{Layer: "code.core", Kind: "Function", ID: "fn:" + sel.Anchors[0].Value},
				Confidence: 0.95,
				ViaAnchor:  "qualified_name",
			}, nil
		},
	}
	c := NewEntityRefCache(fakeResolver)

	sel := SelectorRef{Anchors: []Anchor{{Kind: "qualified_name", Value: "foo.Bar"}, {Kind: "path_glob", Value: "src/foo.go"}}}
	ctx := context.Background()

	// Cold miss populates.
	r1, err := c.Resolve(ctx, sel, 1)
	if err != nil {
		t.Fatalf("Resolve cold: %v", err)
	}
	if r1.Ref.ID != "fn:foo.Bar" {
		t.Fatalf("Resolve cold: got %q, want %q", r1.Ref.ID, "fn:foo.Bar")
	}
	if c.Len() != 1 {
		t.Errorf("Len after cold: got %d, want 1", c.Len())
	}

	// Warm hit returns the same EntityRef without calling the resolver.
	resolverCalls := fakeResolver.calls.Load()
	r2, err := c.Resolve(ctx, sel, 1)
	if err != nil {
		t.Fatalf("Resolve warm: %v", err)
	}
	if r2.Ref != r1.Ref {
		t.Errorf("warm-hit ref mismatch")
	}
	if fakeResolver.calls.Load() != resolverCalls {
		t.Errorf("warm hit unexpectedly called resolver (%d → %d)", resolverCalls, fakeResolver.calls.Load())
	}

	// InvalidateByEntity drops the entry.
	c.InvalidateByEntity("fn:foo.Bar")
	if c.Len() != 0 {
		t.Errorf("InvalidateByEntity didn't clear: Len=%d", c.Len())
	}

	// Repopulate then InvalidateByPath.
	_, _ = c.Resolve(ctx, sel, 1)
	c.InvalidateByPath("src/foo.go")
	if c.Len() != 0 {
		t.Errorf("InvalidateByPath didn't clear: Len=%d", c.Len())
	}

	// Singleflight: spawn N goroutines resolving the same sel
	// against a slow resolver; only one call hits the resolver.
	slow := &fakeResolverImpl{
		resolve: func(_ context.Context, sel SelectorRef, _ uint64) (EntityRef, error) {
			time.Sleep(50 * time.Millisecond)
			return EntityRef{
				Ref:       kernel.EntityRef{Layer: "code.core", Kind: "Function", ID: "fn:" + sel.Anchors[0].Value},
				ViaAnchor: "qualified_name",
			}, nil
		},
	}
	cs := NewEntityRefCache(slow)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = cs.Resolve(ctx, sel, 1)
		}()
	}
	wg.Wait()
	if slow.calls.Load() != 1 {
		t.Errorf("singleflight: resolver called %d times, want 1", slow.calls.Load())
	}

	// Nil resolver yields ErrCacheMiss.
	cn := NewEntityRefCache(nil)
	_, err = cn.Resolve(ctx, sel, 1)
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("nil-resolver: got %v, want ErrCacheMiss", err)
	}
}

type fakeResolverImpl struct {
	resolve func(ctx context.Context, sel SelectorRef, atSeq uint64) (EntityRef, error)
	calls   atomic.Uint64
}

func (f *fakeResolverImpl) Resolve(ctx context.Context, sel SelectorRef, atSeq uint64) (EntityRef, error) {
	f.calls.Add(1)
	return f.resolve(ctx, sel, atSeq)
}

// containsEK is a typed slices.Contains-equivalent for EntityKind.
func containsEK(s []EntityKind, x string) bool {
	for _, v := range s {
		if string(v) == x {
			return true
		}
	}
	return false
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

// newFactsFromLog adapts *EventLog to facts.Facts for the test.
// Production code uses internal/facts/event_log_facts.go's EventLogFacts
// which implements the full interface; we reuse it here.
func newFactsFromLog(log *facts.EventLog) facts.Facts {
	return facts.NewEventLogFacts(log, "code.framework")
}

// Suppress fmt-unused warning (used by future tests that print
// stamped event details).
var _ = fmt.Sprintf
