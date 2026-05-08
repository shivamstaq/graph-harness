package dsl

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func TestParse_SelectorWithSingleAnchor(t *testing.T) {
	src := `
selector CheckoutValidator {
  unique
  anchor qualified_name "CheckoutValidator.Validate"
}
`
	f, err := ParseString("test.gh", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(f.Decls) != 1 || f.Decls[0].Selector == nil {
		t.Fatalf("expected one selector decl, got %+v", f.Decls)
	}
	s := f.Decls[0].Selector
	if s.Name != "CheckoutValidator" {
		t.Errorf("name = %q", s.Name)
	}
	if !s.Unique {
		t.Errorf("unique flag not set")
	}
	if len(s.Anchors) != 1 || s.Anchors[0].Kind != "qualified_name" {
		t.Errorf("anchors = %+v", s.Anchors)
	}
	if s.Anchors[0].Marker != "anchor" {
		t.Errorf("marker = %q, want %q", s.Anchors[0].Marker, "anchor")
	}
}

func TestParse_FlowWithStep(t *testing.T) {
	src := `
flow CheckoutValidation {
  description "Pre-payment cart validation"
  scope CheckoutValidator
  step ValidateCart targets selector { qualified_name "CheckoutValidator.Validate" }
}
`
	f, err := ParseString("test.gh", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	flow := f.Decls[0].Flow
	if flow == nil {
		t.Fatalf("expected flow, got %+v", f.Decls)
	}
	if flow.Name != "CheckoutValidation" {
		t.Errorf("name = %q", flow.Name)
	}
	if len(flow.Steps) != 1 || flow.Steps[0].Name != "ValidateCart" {
		t.Errorf("steps = %+v", flow.Steps)
	}
}

func TestParse_RejectsDatalogRule(t *testing.T) {
	src := `rule reaches_payment(F) :- entity(F, "Function").`
	_, err := ParseString("test.gh", src)
	if err == nil || !strings.Contains(err.Error(), "datalog rules deferred") {
		t.Fatalf("expected Datalog rejection, got %v", err)
	}
}

func TestRoundtrip_SelectorAndFlow(t *testing.T) {
	src := `selector CheckoutValidator {
  unique
  anchor qualified_name "CheckoutValidator.Validate"
}

flow CheckoutValidation {
  description "Pre-payment cart validation"
  scope CheckoutValidator
  step ValidateCart targets selector { qualified_name "CheckoutValidator.Validate" }
}
`
	f, err := ParseString("test.gh", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rendered := Render(f)
	f2, err := ParseString("rendered.gh", rendered)
	if err != nil {
		t.Fatalf("re-parse rendered: %v\n%s", err, rendered)
	}
	if len(f.Decls) != len(f2.Decls) {
		t.Fatalf("decl count differs: orig=%d, rendered=%d", len(f.Decls), len(f2.Decls))
	}
	if f.Decls[0].Selector.Name != f2.Decls[0].Selector.Name {
		t.Errorf("selector name diverged after roundtrip")
	}
	if f.Decls[1].Flow.Name != f2.Decls[1].Flow.Name {
		t.Errorf("flow name diverged after roundtrip")
	}
}

func TestParse_Comments(t *testing.T) {
	src := `
// top comment
selector S { /* inline */ unique anchor qualified_name "X" }
`
	f, err := ParseString("test.gh", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Decls[0].Selector.Name != "S" {
		t.Errorf("comment elision broke parse: %+v", f.Decls)
	}
}

// TestParse_NewAnchorKinds covers every new P1 anchor kind.
func TestParse_NewAnchorKinds(t *testing.T) {
	src := `
selector PaymentAuthorize {
  unique
  anchor qualified_name "payments.PaymentService.authorize"
  anchor function_signature sig(Money, Card) -> AuthResult
  anchor body_hash "sha256:abc123"
  anchor symbol_fingerprint "f:authorize/sig=Money,Card"
  anchor ast_hash "ast:deadbeef"
  anchor call_neighborhood {
    callers: ["CheckoutMutation.handle"]
    callees: ["PaymentGateway.capture", "OrderService.create"]
  }
  fallback path_glob "src/payments/**/payment_service.*"
  thresholds {
    bound: 0.95
    reanchored: 0.75
    ambiguous_zone: [0.55, 0.75]
  }
}
`
	f, err := ParseString("test.gh", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := f.Decls[0].Selector
	if !s.Unique {
		t.Errorf("unique flag not set")
	}
	if len(s.Anchors) != 7 {
		t.Fatalf("expected 7 anchors, got %d", len(s.Anchors))
	}

	wantKinds := []string{
		"qualified_name", "function_signature", "body_hash",
		"symbol_fingerprint", "ast_hash", "call_neighborhood",
		"path_glob",
	}
	for i, k := range wantKinds {
		if s.Anchors[i].Kind != k {
			t.Errorf("anchor[%d].Kind = %q, want %q", i, s.Anchors[i].Kind, k)
		}
	}

	// fallback marker should be preserved on the path_glob anchor (index 6).
	if s.Anchors[6].Marker != "fallback" {
		t.Errorf("expected fallback marker on path_glob anchor, got %q", s.Anchors[6].Marker)
	}

	// function_signature: sig(Money, Card) -> AuthResult.
	sig := s.Anchors[1].Value.Sig
	if sig == nil {
		t.Fatalf("function_signature value missing Sig payload: %+v", s.Anchors[1].Value)
	}
	if len(sig.Params) != 2 || sig.Params[0] != "Money" || sig.Params[1] != "Card" {
		t.Errorf("sig params = %v", sig.Params)
	}
	if sig.Return == nil || sig.Return.Single == nil || *sig.Return.Single != "AuthResult" {
		t.Errorf("sig return = %+v", sig.Return)
	}

	// call_neighborhood (index 5).
	nb := s.Anchors[5].Value.Neighbor
	if nb == nil {
		t.Fatalf("call_neighborhood payload missing")
	}
	if nb.Callers == nil || len(nb.Callers.Items) != 1 || nb.Callers.Items[0] != "CheckoutMutation.handle" {
		t.Errorf("callers = %+v", nb.Callers)
	}
	if nb.Callees == nil || len(nb.Callees.Items) != 2 {
		t.Errorf("callees = %+v", nb.Callees)
	}

	// thresholds block.
	if s.Thresholds == nil {
		t.Fatalf("thresholds block missing")
	}
	if len(s.Thresholds.Items) != 3 {
		t.Errorf("thresholds items = %d, want 3", len(s.Thresholds.Items))
	}
	bound := s.Thresholds.Items[0]
	if bound.Name != "bound" || bound.Float == nil || *bound.Float != 0.95 {
		t.Errorf("bound threshold = %+v", bound)
	}
	zone := s.Thresholds.Items[2]
	if zone.Name != "ambiguous_zone" || zone.Range == nil {
		t.Fatalf("ambiguous_zone threshold = %+v", zone)
	}
	if zone.Range.Lo != 0.55 || zone.Range.Hi != 0.75 {
		t.Errorf("ambiguous_zone range = [%v, %v]", zone.Range.Lo, zone.Range.Hi)
	}
}

// TestParse_FunctionSignatureTupleReturn covers the parenthesized-return form.
func TestParse_FunctionSignatureTupleReturn(t *testing.T) {
	src := `selector S {
  anchor function_signature sig(int, string) -> (int, error)
}`
	f, err := ParseString("test.gh", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sig := f.Decls[0].Selector.Anchors[0].Value.Sig
	if sig == nil || sig.Return == nil || sig.Return.Tuple == nil {
		t.Fatalf("expected tuple return, got %+v", sig)
	}
	if len(sig.Return.Tuple.Items) != 2 ||
		sig.Return.Tuple.Items[0] != "int" || sig.Return.Tuple.Items[1] != "error" {
		t.Errorf("tuple items = %v", sig.Return.Tuple.Items)
	}
}

// TestParse_FunctionSignatureZeroParams covers `sig() -> R`.
func TestParse_FunctionSignatureZeroParams(t *testing.T) {
	src := `selector S { anchor function_signature sig() -> bool }`
	f, err := ParseString("test.gh", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sig := f.Decls[0].Selector.Anchors[0].Value.Sig
	if sig == nil || len(sig.Params) != 0 {
		t.Fatalf("expected zero params, got %+v", sig)
	}
}

// TestParse_CallNeighborhoodEither covers callers-only and callees-only forms.
func TestParse_CallNeighborhoodEither(t *testing.T) {
	cases := []string{
		`selector S { anchor call_neighborhood { callers: ["A"] } }`,
		`selector S { anchor call_neighborhood { callees: ["B"] } }`,
		`selector S { anchor call_neighborhood { } }`,
	}
	for i, src := range cases {
		f, err := ParseString("test.gh", src)
		if err != nil {
			t.Fatalf("case %d parse: %v", i, err)
		}
		nb := f.Decls[0].Selector.Anchors[0].Value.Neighbor
		if nb == nil {
			t.Fatalf("case %d: missing neighbor payload", i)
		}
	}
}

// TestParse_ThresholdIntValue confirms integer-form thresholds round-trip.
func TestParse_ThresholdIntValue(t *testing.T) {
	src := `selector S { thresholds { weight: 1 } }`
	f, err := ParseString("test.gh", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	t0 := f.Decls[0].Selector.Thresholds.Items[0]
	if t0.Int == nil || *t0.Int != 1 {
		t.Errorf("threshold int = %+v", t0)
	}
}

// TestRoundtrip_NewAnchorKinds verifies bidirectional rendering keeps the
// AST byte-stable across every new shape.
func TestRoundtrip_NewAnchorKinds(t *testing.T) {
	src := `selector PaymentAuthorize {
  unique
  anchor qualified_name "payments.PaymentService.authorize"
  anchor function_signature sig(Money, Card) -> AuthResult
  anchor body_hash "sha256:abc"
  anchor symbol_fingerprint "f:authorize/x"
  anchor ast_hash "ast:1234"
  anchor call_neighborhood {
    callers: ["X.a", "X.b"]
    callees: ["Y.c"]
  }
  fallback path_glob "src/**/payment.*"
  thresholds {
    bound: 0.95
    reanchored: 0.75
    ambiguous_zone: [0.55, 0.75]
  }
}
`
	f1, err := ParseString("a.gh", src)
	if err != nil {
		t.Fatalf("parse-1: %v", err)
	}
	r1 := Render(f1)
	f2, err := ParseString("b.gh", r1)
	if err != nil {
		t.Fatalf("parse-2: %v\n--- rendered ---\n%s", err, r1)
	}
	r2 := Render(f2)
	if r1 != r2 {
		t.Fatalf("Render(Parse(Render(Parse(src)))) not byte-stable:\n--- r1 ---\n%s\n--- r2 ---\n%s", r1, r2)
	}
}

// TestRoundtrip_RandomMultiAnchor is the property test mandated by P1.T24.
// 100 random multi-anchor selectors must Parse → Render → Parse to a byte-
// identical AST (we compare via the second-pass Render, which canonicalizes
// formatting; that's the strict round-trip property).
func TestRoundtrip_RandomMultiAnchor(t *testing.T) {
	rng := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic test seed
	const n = 100
	for i := range n {
		src := generateRandomSelector(rng, i)
		f1, err := ParseString(fmt.Sprintf("rand-%d.gh", i), src)
		if err != nil {
			t.Fatalf("case %d parse-1 failed: %v\n--- src ---\n%s", i, err, src)
		}
		r1 := Render(f1)
		f2, err := ParseString(fmt.Sprintf("rand-%d-r1.gh", i), r1)
		if err != nil {
			t.Fatalf("case %d parse-2 failed: %v\n--- r1 ---\n%s", i, err, r1)
		}
		r2 := Render(f2)
		if r1 != r2 {
			t.Fatalf("case %d not byte-stable across roundtrip:\n--- r1 ---\n%s\n--- r2 ---\n%s", i, r1, r2)
		}
		// Also assert anchor count and ordering survive.
		if len(f1.Decls[0].Selector.Anchors) != len(f2.Decls[0].Selector.Anchors) {
			t.Fatalf("case %d anchor count drift: %d → %d", i,
				len(f1.Decls[0].Selector.Anchors),
				len(f2.Decls[0].Selector.Anchors))
		}
		for k := range f1.Decls[0].Selector.Anchors {
			a1, a2 := f1.Decls[0].Selector.Anchors[k], f2.Decls[0].Selector.Anchors[k]
			if a1.Kind != a2.Kind || a1.Marker != a2.Marker {
				t.Fatalf("case %d anchor[%d] divergence: %+v vs %+v", i, k, a1, a2)
			}
		}
	}
}

// generateRandomSelector emits a syntactically valid selector exercising the
// full P1 anchor surface. The shape is deliberately varied so coverage hits
// every value-type branch and the optional thresholds block.
func generateRandomSelector(rng *rand.Rand, idx int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "selector RandSel%d {\n", idx)
	if rng.Intn(2) == 0 {
		b.WriteString("  unique\n")
	}
	// 2..7 anchors per selector to exercise multi-anchor.
	count := 2 + rng.Intn(6)
	kinds := []string{
		"qualified_name", "body_hash", "symbol_fingerprint", "ast_hash",
		"path_glob", "function_signature", "call_neighborhood",
	}
	for i := range count {
		marker := "anchor"
		if rng.Intn(5) == 0 {
			marker = "fallback"
		}
		kind := kinds[rng.Intn(len(kinds))]
		switch kind {
		case "function_signature":
			fmt.Fprintf(&b, "  %s %s %s\n", marker, kind, randomSignature(rng))
		case "call_neighborhood":
			fmt.Fprintf(&b, "  %s %s %s\n", marker, kind, randomCallNeighborhood(rng))
		default:
			fmt.Fprintf(&b, "  %s %s %q\n", marker, kind, randomStringValue(rng, kind, i))
		}
	}
	if rng.Intn(2) == 0 {
		b.WriteString("  thresholds {\n")
		b.WriteString("    bound: 0.95\n")
		b.WriteString("    reanchored: 0.75\n")
		if rng.Intn(2) == 0 {
			b.WriteString("    ambiguous_zone: [0.55, 0.75]\n")
		}
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func randomStringValue(rng *rand.Rand, kind string, salt int) string {
	switch kind {
	case "path_glob":
		return fmt.Sprintf("src/pkg%d/**/*.go", salt)
	case "body_hash":
		return fmt.Sprintf("sha256:%016x", rng.Int63())
	case "symbol_fingerprint":
		return fmt.Sprintf("f:fn%d/sig=A,B", salt)
	case "ast_hash":
		return fmt.Sprintf("ast:%016x", rng.Int63())
	default: // qualified_name
		return fmt.Sprintf("pkg%d.Type%d.method%d", salt, salt+1, salt+2)
	}
}

func randomSignature(rng *rand.Rand) string {
	tps := []string{"int", "string", "Money", "Card", "User", "Order", "AuthResult"}
	pick := func() string { return tps[rng.Intn(len(tps))] }
	pCount := rng.Intn(4) // 0..3 params
	var ps []string
	for i := 0; i < pCount; i++ {
		ps = append(ps, pick())
	}
	if rng.Intn(2) == 0 {
		// tuple return
		rs := []string{pick(), "error"}
		return fmt.Sprintf("sig(%s) -> (%s)", strings.Join(ps, ", "), strings.Join(rs, ", "))
	}
	return fmt.Sprintf("sig(%s) -> %s", strings.Join(ps, ", "), pick())
}

func randomCallNeighborhood(rng *rand.Rand) string {
	var b strings.Builder
	b.WriteString("{\n")
	if rng.Intn(2) == 0 {
		b.WriteString(`    callers: ["A.x", "B.y"]` + "\n")
	}
	if rng.Intn(2) == 0 {
		b.WriteString(`    callees: ["C.z"]` + "\n")
	}
	b.WriteString("  }")
	return b.String()
}
