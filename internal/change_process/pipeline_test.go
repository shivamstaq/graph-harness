package change_process

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/dsl"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/semantic_overlay"
)

func newTestPipeline(t *testing.T) (*Pipeline, *code_core.Store, *semantic_overlay.Overlay, *facts.EventLog) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.db")
	log, err := facts.OpenEventLog(logPath)
	if err != nil {
		t.Fatalf("OpenEventLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := code_core.NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	overlay := semantic_overlay.NewOverlay()
	resolver, err := semantic_overlay.NewResolver(overlay, store, nil)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	return &Pipeline{
		Overlay:  overlay,
		Code:     store,
		Resolver: resolver,
		Events:   log,
	}, store, overlay, log
}

func parseSelector(t *testing.T, src string) *dsl.Selector {
	t.Helper()
	f, err := dsl.ParseString("test.gh", src)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	return f.Decls[0].Selector
}

func TestPipeline_EmitsSymbolDisambiguationForCodeCoreEvent(t *testing.T) {
	p, _, _, log := newTestPipeline(t)
	// Append a code.core.SymbolDisambiguation event the unifier would emit.
	payload, _ := json.Marshal(map[string]any{
		"qualified_name": "pkg.Foo",
		"sources": []map[string]any{
			{"source_class": "live_lsp", "produced_by": "gopls", "signature": "(int) -> int"},
			{"source_class": "index_scip", "produced_by": "scip-go", "signature": "(int) -> error"},
		},
	})
	if _, err := log.Append(context.Background(), []kernel.Event{{
		TS:         time.Now().UTC(),
		Layer:      "code.core",
		Kind:       "SymbolDisambiguation",
		Subject:    &kernel.EntityRef{Layer: "code.core", Kind: "Symbol", ID: "sym1"},
		Payload:    payload,
		ProducedBy: kernel.SourceExtractorLSP,
	}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	res, err := p.ValidateDiff(context.Background(), []byte("--- a/x\n+++ b/x\n"), log.LastSeq())
	if err != nil {
		t.Fatalf("ValidateDiff: %v", err)
	}
	found := 0
	for _, f := range res.Findings {
		if f.Kind == "symbol_disambiguation" {
			found++
			if f.Subject.Qualified != "pkg.Foo" {
				t.Errorf("subject.qualified = %q, want pkg.Foo", f.Subject.Qualified)
			}
			if len(f.Evidence) == 0 {
				t.Errorf("evidence missing")
			}
		}
	}
	if found != 1 {
		t.Errorf("expected 1 symbol_disambiguation finding, got %d", found)
	}
}

func TestPipeline_EmitsUnresolvedAnchorWhenBindingBreaks(t *testing.T) {
	p, store, overlay, _ := newTestPipeline(t)
	// Selector targets `pkg.Authorize`. Pre-existing entity matches; we
	// resolve once at seq=10 to seed a `bound` cache entry.
	if err := store.PutEntity(context.Background(), code_core.Entity{
		ID: "f1", Kind: code_core.KindFunction, LanguageID: "go",
		QualifiedName: "pkg.Authorize",
	}, 1); err != nil {
		t.Fatalf("PutEntity: %v", err)
	}
	overlay.Selectors["AuthSel"] = parseSelector(t, `selector AuthSel {
		anchor qualified_name "pkg.Authorize"
	}`)
	if env, _, err := p.Resolver.Resolve(context.Background(), "AuthSel", 10); err != nil {
		t.Fatalf("seed Resolve: %v", err)
	} else if env.Outcome != semantic_overlay.OutcomeBound {
		t.Fatalf("seed outcome = %s, want bound", env.Outcome)
	}

	// Now simulate the diff that broke the binding: drop the entity.
	if err := store.DeleteEntity(context.Background(), "f1"); err != nil {
		t.Fatalf("delete entity: %v", err)
	}

	res, err := p.ValidateDiff(context.Background(), []byte("--- a/x\n+++ b/x\n"), 20)
	if err != nil {
		t.Fatalf("ValidateDiff: %v", err)
	}
	found := 0
	for _, f := range res.Findings {
		if f.Kind == "unresolved_anchor" {
			found++
			if f.Subject.EntityID != "AuthSel" {
				t.Errorf("subject.entity_id = %q, want AuthSel", f.Subject.EntityID)
			}
		}
	}
	if found != 1 {
		t.Errorf("expected 1 unresolved_anchor finding, got %d", found)
	}
}

func TestPipeline_NoUnresolvedAnchorWhenStillResolved(t *testing.T) {
	p, store, overlay, _ := newTestPipeline(t)
	if err := store.PutEntity(context.Background(), code_core.Entity{
		ID: "f1", Kind: code_core.KindFunction, LanguageID: "go",
		QualifiedName: "pkg.Stable",
	}, 1); err != nil {
		t.Fatalf("PutEntity: %v", err)
	}
	overlay.Selectors["S"] = parseSelector(t, `selector S {
		anchor qualified_name "pkg.Stable"
	}`)
	_, _, _ = p.Resolver.Resolve(context.Background(), "S", 10)

	res, err := p.ValidateDiff(context.Background(), []byte("--- a/x\n+++ b/x\n"), 20)
	if err != nil {
		t.Fatalf("ValidateDiff: %v", err)
	}
	for _, f := range res.Findings {
		if f.Kind == "unresolved_anchor" {
			t.Fatalf("unexpected unresolved_anchor finding: %+v", f)
		}
	}
}

// TestPipeline_EmitsSelectorReanchoredOnRename exercises plan §3 gate
// criterion 6 at the pipeline level: a fixture where the renamed function
// keeps its body_hash (and signature), so the resolver re-binds via
// fingerprint fallback. The pipeline must emit a `selector_reanchored`
// finding with confidence ≥ 0.75 and via_anchor identifying the fingerprint
// that paid for the rebind.
func TestPipeline_EmitsSelectorReanchoredOnRename(t *testing.T) {
	p, store, overlay, _ := newTestPipeline(t)
	// Renamed entity: original name `validate` → `preValidate`, body_hash
	// + normalized signature unchanged.
	if err := store.PutEntity(context.Background(), code_core.Entity{
		ID: "rn1", Kind: code_core.KindMethod, LanguageID: "typescript",
		QualifiedName:       "CheckoutValidator.preValidate",
		NormalizedSignature: "(cart: Cart) => void",
		BodyHash:            "sha256:body-of-validate",
	}, 1); err != nil {
		t.Fatalf("PutEntity: %v", err)
	}
	overlay.Selectors["CheckoutValidator"] = parseSelector(t, `selector CheckoutValidator {
		anchor qualified_name "CheckoutValidator.validate"
		anchor function_signature sig(Cart) -> void
		anchor body_hash "sha256:body-of-validate"
	}`)

	res, err := p.ValidateDiff(context.Background(), []byte("--- a/x\n+++ b/x\n"), 10)
	if err != nil {
		t.Fatalf("ValidateDiff: %v", err)
	}
	var hit *ValidationFinding
	for i, f := range res.Findings {
		if f.Kind == "selector_reanchored" {
			hit = &res.Findings[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("expected selector_reanchored finding; got %+v", res.Findings)
	}
	if hit.Subject.EntityID != "CheckoutValidator" {
		t.Errorf("subject.entity_id = %s, want CheckoutValidator", hit.Subject.EntityID)
	}
	if len(hit.Evidence) == 0 {
		t.Fatal("evidence missing")
	}
	d := hit.Evidence[0].Detail
	if !strings.Contains(d, "outcome=reanchored") {
		t.Errorf("evidence detail missing outcome=reanchored: %q", d)
	}
	if !strings.Contains(d, "anchor=function_signature") &&
		!strings.Contains(d, "anchor=body_hash") {
		t.Errorf("evidence detail missing fingerprint anchor: %q", d)
	}
	// Gate criterion 6: confidence ≥ 0.75. function_signature defaults to
	// 0.85 and body_hash to 0.95 — either crosses the threshold. The detail
	// string carries `confidence=X.XX`; we parse it back out for the assert.
	var confidence float64
	for _, m := range hit.Evidence {
		if i := strings.Index(m.Detail, "confidence="); i >= 0 {
			s := m.Detail[i+len("confidence="):]
			// next 4 chars are the number ("0.85")
			if len(s) >= 4 {
				if v, err := parseFloatPrefix(s); err == nil {
					confidence = v
				}
			}
		}
	}
	if confidence < 0.75 {
		t.Errorf("reanchored confidence %.2f < 0.75 (gate criterion 6)", confidence)
	}
}

// parseFloatPrefix reads the leading float (one digit, dot, then digits)
// from s. Local helper for the assert above; avoids pulling regex.
func parseFloatPrefix(s string) (float64, error) {
	end := 0
	for end < len(s) && (s[end] == '.' || (s[end] >= '0' && s[end] <= '9')) {
		end++
	}
	if end == 0 {
		return 0, errBadFloatPrefix
	}
	var f float64
	if _, err := fmt.Sscanf(s[:end], "%f", &f); err != nil {
		return 0, err
	}
	return f, nil
}

var errBadFloatPrefix = fmt.Errorf("no float prefix")

func TestPipeline_NoFindingsWhenResolverNil(t *testing.T) {
	// P0 callers without a resolver still get the flow_unreviewed path.
	dir := t.TempDir()
	log, _ := facts.OpenEventLog(filepath.Join(dir, "events.db"))
	defer func() { _ = log.Close() }()
	db, _ := sql.Open("sqlite", ":memory:")
	defer func() { _ = db.Close() }()
	store, _ := code_core.NewStore(db)
	p := &Pipeline{Overlay: semantic_overlay.NewOverlay(), Code: store}
	res, err := p.ValidateDiff(context.Background(), []byte("--- a/x\n+++ b/x\n"), 1)
	if err != nil {
		t.Fatalf("ValidateDiff: %v", err)
	}
	for _, f := range res.Findings {
		if f.Kind == "unresolved_anchor" || f.Kind == "symbol_disambiguation" {
			t.Errorf("unexpected P1 finding %s when sources nil: %+v", f.Kind, f)
		}
	}
}

// ---------------------------------------------------------------------------
// P2.T36 framework-edge propagation tests.
//
// Touch model: the test seeds a code.core Function whose qualified name
// matches a Go function decl on the diff's added lines (so Stage 2's
// suffix lookup finds it). A selector binding pulls in the
// framework producer (EventPublisher / SchemaField / Route) via the
// reverse index. Dependents share the producer's qualified_name and
// are discovered via Store.LookupAllByQualifiedName at Stage 5.
// ---------------------------------------------------------------------------

// touchDiff builds the minimal unified diff that makes Stage 2's
// extractFunctionNames produce `funcName` so a seeded Function entity
// with `qualified_name = "pkg.<funcName>"` is added to the touched
// set via LookupByQualifiedNameSuffix.
func touchDiff(funcName string) string {
	return "--- a/x.go\n+++ b/x.go\n@@\n+func " + funcName + "() {}\n"
}

// seedProducerWithTouch wires a touched Function + a framework producer
// entity sharing a selector binding. Returns the producer entity ID
// so the test can assert it shows up as the finding's subject.
func seedProducerWithTouch(t *testing.T, store *code_core.Store,
	funcName, producerKind, producerQN string) string {
	t.Helper()
	ctx := context.Background()

	// Touched Function (qualified_name ends with funcName so the
	// suffix lookup matches).
	funcID := "fn:" + funcName
	if err := store.PutEntity(ctx, code_core.Entity{
		ID:            funcID,
		Kind:          code_core.KindFunction,
		LanguageID:    "go",
		QualifiedName: "pkg." + funcName,
	}, 1); err != nil {
		t.Fatalf("PutEntity func: %v", err)
	}

	// Framework producer entity, sharing-key in qualified_name.
	producerID := producerKind + ":" + producerQN
	if err := store.PutEntity(ctx, code_core.Entity{
		ID:            producerID,
		Kind:          code_core.EntityKind(producerKind),
		LanguageID:    "framework",
		QualifiedName: producerQN,
	}, 1); err != nil {
		t.Fatalf("PutEntity producer: %v", err)
	}

	// Reverse-index binding: one selector bound to both the function
	// and the producer entity. Stage 5 walks func → selector →
	// producer via this hop.
	sel := "sel:" + producerID
	if err := store.BindSelector(ctx, funcID, sel, "", "qualified_name", 1); err != nil {
		t.Fatalf("BindSelector func: %v", err)
	}
	if err := store.BindSelector(ctx, producerID, sel, "", "framework_anchor", 1); err != nil {
		t.Fatalf("BindSelector producer: %v", err)
	}
	return producerID
}

// seedDependent inserts a framework dependent entity that shares the
// producer's linking qualified_name. Stage 5 enumerates these via
// Store.LookupAllByQualifiedName(producerQN).
func seedDependent(t *testing.T, store *code_core.Store,
	id, kind, qn string) {
	t.Helper()
	if err := store.PutEntity(context.Background(), code_core.Entity{
		ID:            id,
		Kind:          code_core.EntityKind(kind),
		LanguageID:    "framework",
		QualifiedName: qn,
	}, 1); err != nil {
		t.Fatalf("PutEntity dependent %s: %v", id, err)
	}
}

// findOne returns the single finding of the named kind, failing the
// test if zero or multiple matches exist.
func findOne(t *testing.T, findings []ValidationFinding, kind string) ValidationFinding {
	t.Helper()
	var hits []ValidationFinding
	for _, f := range findings {
		if f.Kind == kind {
			hits = append(hits, f)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("expected exactly 1 %s finding, got %d (all=%+v)", kind, len(hits), findings)
	}
	return hits[0]
}

func TestPipeline_MissingDependentUpdate_EventPublisher(t *testing.T) {
	p, store, _, _ := newTestPipeline(t)
	producerID := seedProducerWithTouch(t, store, "PublishOrder",
		"EventPublisher", "kafka:order.created")
	seedDependent(t, store, "sub:svc-a", "EventSubscriber", "kafka:order.created")
	seedDependent(t, store, "sub:svc-b", "EventSubscriber", "kafka:order.created")
	seedDependent(t, store, "ctest:order", "ContractTest", "kafka:order.created")

	res, err := p.ValidateDiff(context.Background(),
		[]byte(touchDiff("PublishOrder")), 10)
	if err != nil {
		t.Fatalf("ValidateDiff: %v", err)
	}
	f := findOne(t, res.Findings, "missing_dependent_update")
	if f.Subject.EntityID != producerID {
		t.Errorf("subject.entity_id = %q, want %q", f.Subject.EntityID, producerID)
	}
	if f.Subject.EntityKind != "code.framework:EventPublisher" {
		t.Errorf("subject.entity_kind = %q, want code.framework:EventPublisher", f.Subject.EntityKind)
	}
	if f.Severity != "high" {
		t.Errorf("severity = %q, want high", f.Severity)
	}
	if len(f.Evidence) != 3 {
		t.Errorf("evidence count = %d, want 3 (2 subscribers + 1 contract test)", len(f.Evidence))
	}
	// FrameworkContext (P2.T37) is populated.
	if f.FrameworkContext == nil {
		t.Fatal("FrameworkContext nil; P2.T37 context injection missing")
	}
	if f.FrameworkContext.TouchedKind != "EventPublisher" {
		t.Errorf("framework_context.touched_kind = %q", f.FrameworkContext.TouchedKind)
	}
	if len(f.FrameworkContext.Dependents) != 3 {
		t.Errorf("framework_context dependents = %d, want 3", len(f.FrameworkContext.Dependents))
	}
}

func TestPipeline_MissingDependentUpdate_SchemaField(t *testing.T) {
	p, store, _, _ := newTestPipeline(t)
	producerID := seedProducerWithTouch(t, store, "UpdateEmail",
		"SchemaField", "users.email")
	seedDependent(t, store, "read:users.email:a", "SchemaRead", "users.email")
	seedDependent(t, store, "read:users.email:b", "SchemaRead", "users.email")
	seedDependent(t, store, "write:users.email:a", "SchemaWrite", "users.email")
	seedDependent(t, store, "test:users.email", "Test", "users.email")

	res, err := p.ValidateDiff(context.Background(),
		[]byte(touchDiff("UpdateEmail")), 10)
	if err != nil {
		t.Fatalf("ValidateDiff: %v", err)
	}
	f := findOne(t, res.Findings, "missing_dependent_update")
	if f.Subject.EntityID != producerID {
		t.Errorf("subject.entity_id = %q, want %q", f.Subject.EntityID, producerID)
	}
	if f.Severity != "high" {
		t.Errorf("severity = %q, want high (reads + writes present)", f.Severity)
	}
	if len(f.Evidence) != 4 {
		t.Errorf("evidence count = %d, want 4 (2 reads + 1 write + 1 test)", len(f.Evidence))
	}
	// Every evidence detail is "<kind>:<qualified_name>".
	for _, e := range f.Evidence {
		if e.Kind != "missing_dependent_update" {
			t.Errorf("evidence.kind = %q, want missing_dependent_update", e.Kind)
		}
		if !strings.Contains(e.Detail, "users.email") {
			t.Errorf("evidence.detail = %q missing producer qn", e.Detail)
		}
	}
}

func TestPipeline_MissingDependentUpdate_Route(t *testing.T) {
	p, store, _, _ := newTestPipeline(t)
	producerID := seedProducerWithTouch(t, store, "HandleGetUser",
		"Route", "GET /users/{id}")
	seedDependent(t, store, "handler:get-user", "Handler", "GET /users/{id}")
	seedDependent(t, store, "ctest:get-user", "ContractTest", "GET /users/{id}")

	res, err := p.ValidateDiff(context.Background(),
		[]byte(touchDiff("HandleGetUser")), 10)
	if err != nil {
		t.Fatalf("ValidateDiff: %v", err)
	}
	f := findOne(t, res.Findings, "missing_dependent_update")
	if f.Subject.EntityID != producerID {
		t.Errorf("subject.entity_id = %q, want %q", f.Subject.EntityID, producerID)
	}
	if f.Severity != "medium" {
		t.Errorf("severity = %q, want medium", f.Severity)
	}
	if len(f.Evidence) != 2 {
		t.Errorf("evidence count = %d, want 2 (handler + contract test)", len(f.Evidence))
	}
}

// TestPipeline_MissingDependentUpdate_Idempotent re-runs the same
// diff twice and asserts byte-identical finding JSON. Locks in the
// SPEC §8.1 idempotence contract for the new finding kind.
func TestPipeline_MissingDependentUpdate_Idempotent(t *testing.T) {
	p, store, _, _ := newTestPipeline(t)
	seedProducerWithTouch(t, store, "PublishOrder",
		"EventPublisher", "kafka:order.created")
	seedDependent(t, store, "sub:svc-a", "EventSubscriber", "kafka:order.created")
	seedDependent(t, store, "sub:svc-b", "EventSubscriber", "kafka:order.created")
	seedDependent(t, store, "ctest:order", "ContractTest", "kafka:order.created")

	diff := []byte(touchDiff("PublishOrder"))
	first, err := p.ValidateDiff(context.Background(), diff, 10)
	if err != nil {
		t.Fatalf("ValidateDiff 1: %v", err)
	}
	second, err := p.ValidateDiff(context.Background(), diff, 10)
	if err != nil {
		t.Fatalf("ValidateDiff 2: %v", err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Errorf("idempotence violated:\nfirst=%s\nsecond=%s", a, b)
	}
}

// TestPipeline_MissingDependentUpdate_NoFindingWhenDependentsAlsoTouched
// confirms the stale-only filter: when every dependent is also in the
// touched set, no finding fires. We touch the subscriber by giving its
// underlying entity ID a matching qualified-name suffix (the Stage 2
// lookup keys on `qualified_name`, so a touched function whose
// qualified_name suffix is "HandleOrder" pulls in the subscriber
// entity whose ID matches via a co-bound selector).
func TestPipeline_MissingDependentUpdate_NoFindingWhenDependentsAlsoTouched(t *testing.T) {
	p, store, _, _ := newTestPipeline(t)
	producerID := seedProducerWithTouch(t, store, "PublishOrder",
		"EventPublisher", "kafka:order.created")

	// One subscriber. Bind a selector to BOTH a touched function and
	// the subscriber so the reverse index pulls the subscriber into
	// the touched set when the function is in the diff.
	subID := "sub:order.handler"
	seedDependent(t, store, subID, "EventSubscriber", "kafka:order.created")
	subFuncID := "fn:HandleOrder"
	if err := store.PutEntity(context.Background(), code_core.Entity{
		ID:            subFuncID,
		Kind:          code_core.KindFunction,
		LanguageID:    "go",
		QualifiedName: "pkg.HandleOrder",
	}, 1); err != nil {
		t.Fatalf("PutEntity touched-dependent func: %v", err)
	}
	subSel := "sel:sub:order.handler"
	if err := store.BindSelector(context.Background(), subFuncID, subSel, "", "qualified_name", 1); err != nil {
		t.Fatalf("BindSelector subfunc: %v", err)
	}
	if err := store.BindSelector(context.Background(), subID, subSel, "", "framework_anchor", 1); err != nil {
		t.Fatalf("BindSelector subscriber: %v", err)
	}

	// Diff touches both the publisher's Go func AND the subscriber's
	// Go func. The reverse index pulls both framework entities into
	// the touched set, so no finding should fire.
	diff := []byte(touchDiff("PublishOrder") + touchDiff("HandleOrder"))
	res, err := p.ValidateDiff(context.Background(), diff, 10)
	if err != nil {
		t.Fatalf("ValidateDiff: %v", err)
	}
	for _, f := range res.Findings {
		if f.Kind == "missing_dependent_update" {
			t.Errorf("unexpected missing_dependent_update when dependent also touched: %+v", f)
		}
	}
	_ = producerID
}
