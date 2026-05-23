package dsl

import (
	"fmt"
	"strings"
	"testing"
)

// TestAnchorKind_RoundtripAll exercises every accepted anchor kind via the
// top-level `anchor <kind> <value>` form. Each kind round-trips:
//
//	parse → render → parse → assert AST equality
//
// New kinds added in Phase 2 Pass 0.5 (P2.T33) must appear here.
func TestAnchorKind_RoundtripAll(t *testing.T) {
	cases := []struct {
		kind  string
		value string
	}{
		// Existing P0/P1 kinds.
		{AnchorKindQualifiedName, `"pkg.Type.Method"`},
		{AnchorKindFunctionSignature, `"(int, string) error"`},
		{AnchorKindBodyHash, `"sha256:deadbeef"`},
		{AnchorKindCallNeighborhood, `"pkg.Helper"`},
		{AnchorKindSymbolFingerprint, `"fp-abc123"`},
		{AnchorKindASTHash, `"ast-xyz789"`},
		{AnchorKindPathGlob, `"internal/**/*.go"`},
		// Phase 2 kindwise / framework kinds.
		{AnchorKindEntityKind, `"Route"`},
		{AnchorKindRoutePattern, `"/api/v1/users/:id"`},
		{AnchorKindRouteMethod, `"POST"`},
		{AnchorKindEventName, `"checkout.completed"`},
		{AnchorKindTopicName, `"orders.events"`},
		{AnchorKindSchemaField, `"users.email"`},
		{AnchorKindSchemaTable, `"users"`},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			src := fmt.Sprintf("selector S {\n  anchor %s %s\n}\n", tc.kind, tc.value)
			f1, err := ParseString("a.gh", src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			rendered := Render(f1)
			f2, err := ParseString("rendered.gh", rendered)
			if err != nil {
				t.Fatalf("re-parse rendered: %v\nrendered=%s", err, rendered)
			}
			if len(f1.Decls) != 1 || len(f2.Decls) != 1 {
				t.Fatalf("decl count mismatch")
			}
			if f1.Decls[0].Selector.Anchors[0].Kind != f2.Decls[0].Selector.Anchors[0].Kind {
				t.Errorf("kind mismatch after roundtrip: %q vs %q",
					f1.Decls[0].Selector.Anchors[0].Kind,
					f2.Decls[0].Selector.Anchors[0].Kind)
			}
			v1 := f1.Decls[0].Selector.Anchors[0].Value
			v2 := f2.Decls[0].Selector.Anchors[0].Value
			if !litEqual(v1, v2) {
				t.Errorf("value mismatch after roundtrip: %+v vs %+v", v1, v2)
			}
		})
	}
}

// TestInlineAnchorKind_RoundtripAll exercises every accepted anchor kind in
// the inline-selector form (`step S targets selector { ... }`).
func TestInlineAnchorKind_RoundtripAll(t *testing.T) {
	cases := []struct {
		kind  string
		value string
	}{
		{AnchorKindQualifiedName, `"pkg.Fn"`},
		{AnchorKindEntityKind, `"Route"`},
		{AnchorKindRoutePattern, `"/healthz"`},
		{AnchorKindRouteMethod, `"GET"`},
		{AnchorKindEventName, `"order.placed"`},
		{AnchorKindTopicName, `"orders"`},
		{AnchorKindSchemaField, `"orders.total"`},
		{AnchorKindSchemaTable, `"orders"`},
		{AnchorKindPathGlob, `"cmd/**"`},
	}
	for _, tc := range cases {
		t.Run("inline_"+tc.kind, func(t *testing.T) {
			src := fmt.Sprintf("flow F {\n  step S targets selector { %s %s }\n}\n", tc.kind, tc.value)
			f1, err := ParseString("a.gh", src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			rendered := Render(f1)
			f2, err := ParseString("rendered.gh", rendered)
			if err != nil {
				t.Fatalf("re-parse rendered: %v\nrendered=%s", err, rendered)
			}
			a1 := f1.Decls[0].Flow.Steps[0].Targets.InlineSelector.Anchors[0]
			a2 := f2.Decls[0].Flow.Steps[0].Targets.InlineSelector.Anchors[0]
			if a1.Kind != a2.Kind {
				t.Errorf("inline kind mismatch: %q vs %q", a1.Kind, a2.Kind)
			}
			if !litEqual(a1.Value, a2.Value) {
				t.Errorf("inline value mismatch")
			}
		})
	}
}

// TestInlineAnchor_MultipleWithSemicolon exercises the framework example
// from P2.T33: `targets selector { kind "X"; event_name "Y" }` where the
// `;` separator is optional but commonly written for readability.
func TestInlineAnchor_MultipleWithSemicolon(t *testing.T) {
	sources := []string{
		`flow F { step S targets selector { qualified_name "A.B"; event_name "x" } }`,
		`flow F { step S targets selector { qualified_name "A.B" event_name "x" } }`,
		`flow F { step S targets selector { qualified_name "A.B" ; event_name "x" ; route_pattern "/p" } }`,
	}
	for _, src := range sources {
		f, err := ParseString("t.gh", src)
		if err != nil {
			t.Fatalf("parse %q: %v", src, err)
		}
		anchors := f.Decls[0].Flow.Steps[0].Targets.InlineSelector.Anchors
		if len(anchors) < 2 {
			t.Errorf("expected >=2 anchors in %q, got %d", src, len(anchors))
		}
	}
}

// TestAnchorKind_RejectsUnknown asserts that an unknown anchor kind yields
// a clear, file:line-positioned parse error.
func TestAnchorKind_RejectsUnknown(t *testing.T) {
	src := "selector S {\n  anchor unknown_kind \"x\"\n}\n"
	_, err := ParseString("bad.gh", src)
	if err == nil {
		t.Fatalf("expected unknown-anchor error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown anchor kind") {
		t.Errorf("error missing 'unknown anchor kind': %v", err)
	}
	if !strings.Contains(msg, `"unknown_kind"`) {
		t.Errorf("error missing offending kind: %v", err)
	}
	if !strings.Contains(msg, "bad.gh") {
		t.Errorf("error missing filename: %v", err)
	}
	if !strings.Contains(msg, ":2:") {
		t.Errorf("error missing line:column (want :2:): %v", err)
	}
}

// TestInlineAnchorKind_RejectsUnknown asserts the inline form also surfaces
// unknown-kind errors with positions.
func TestInlineAnchorKind_RejectsUnknown(t *testing.T) {
	src := "flow F {\n  step S targets selector { not_a_real_kind \"x\" }\n}\n"
	_, err := ParseString("bad.gh", src)
	if err == nil {
		t.Fatalf("expected unknown-anchor error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown anchor kind") || !strings.Contains(msg, "not_a_real_kind") {
		t.Errorf("unhelpful error: %v", err)
	}
	if !strings.Contains(msg, "bad.gh:2") {
		t.Errorf("error missing file:line: %v", err)
	}
}

// TestIsValidAnchorKind probes the predicate directly.
func TestIsValidAnchorKind(t *testing.T) {
	for k := range validAnchorKinds {
		if !IsValidAnchorKind(k) {
			t.Errorf("%q reported invalid by IsValidAnchorKind", k)
		}
	}
	if IsValidAnchorKind("definitely_not_a_kind") {
		t.Errorf("bogus kind reported valid")
	}
	if IsValidAnchorKind("") {
		t.Errorf("empty string reported valid")
	}
}

// litEqual compares AnchorValue payloads field-by-field (positions
// excluded). Originally written against the master-version *Lit type;
// updated to the impl/phase2 *AnchorValue shape — the Str/Int/Float
// fields are identical so the comparison is unchanged.
func litEqual(a, b *AnchorValue) bool {
	if a == nil || b == nil {
		return a == b
	}
	if (a.Str == nil) != (b.Str == nil) {
		return false
	}
	if a.Str != nil && *a.Str != *b.Str {
		return false
	}
	if (a.Int == nil) != (b.Int == nil) {
		return false
	}
	if a.Int != nil && *a.Int != *b.Int {
		return false
	}
	if (a.Float == nil) != (b.Float == nil) {
		return false
	}
	if a.Float != nil && *a.Float != *b.Float {
		return false
	}
	return true
}
