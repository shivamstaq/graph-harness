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
