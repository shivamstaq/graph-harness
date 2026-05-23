package dsl

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"testing/quick"
)

// allAnchorKinds is the closed enum from anchor_kinds.go iterated in a
// stable order. We avoid ranging over a map directly so the property
// driver is deterministic given a fixed seed.
var allAnchorKinds = []string{
	AnchorKindQualifiedName,
	AnchorKindFunctionSignature,
	AnchorKindBodyHash,
	AnchorKindCallNeighborhood,
	AnchorKindSymbolFingerprint,
	AnchorKindASTHash,
	AnchorKindPathGlob,
	AnchorKindEntityKind,
	AnchorKindRoutePattern,
	AnchorKindRouteMethod,
	AnchorKindEventName,
	AnchorKindTopicName,
	AnchorKindSchemaField,
	AnchorKindSchemaTable,
}

// genAnchor produces a random valid anchor (kind from the closed enum,
// string value). All 14 kinds shipped through Phase 2 Pass 0.5 accept a
// string literal, which keeps the generator simple while still covering
// every kind.
func genAnchor(rng *rand.Rand) (kind, value string) {
	kind = allAnchorKinds[rng.Intn(len(allAnchorKinds))]
	// Values are alphanumeric + a few path/topic separators so they
	// survive Go string quoting roundtrip identically.
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._/:-"
	n := 1 + rng.Intn(24)
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteByte(alphabet[rng.Intn(len(alphabet))])
	}
	value = sb.String()
	return
}

// genSelector emits a random valid `selector` declaration containing
// 1..4 anchors. The renderer always produces a multi-line form, so
// `single-line forms` from the task brief are covered via the inline
// generator below.
func genSelector(rng *rand.Rand, name string) string {
	n := 1 + rng.Intn(4)
	var sb strings.Builder
	fmt.Fprintf(&sb, "selector %s {\n", name)
	if rng.Intn(2) == 0 {
		sb.WriteString("  unique\n")
	}
	for i := 0; i < n; i++ {
		k, v := genAnchor(rng)
		fmt.Fprintf(&sb, "  anchor %s %q\n", k, v)
	}
	sb.WriteString("}\n")
	return sb.String()
}

// genInlineSelectorFlow emits a `flow` whose step targets an inline
// selector with 1..4 anchors. We randomly choose between the multi-line
// renderer-style output and a single-line `;`-separated form (the task
// brief requires both shapes).
func genInlineSelectorFlow(rng *rand.Rand, name string) string {
	n := 1 + rng.Intn(4)
	anchors := make([]string, n)
	for i := 0; i < n; i++ {
		k, v := genAnchor(rng)
		anchors[i] = fmt.Sprintf("%s %q", k, v)
	}
	if rng.Intn(2) == 0 {
		// Single-line, `;`-separated form.
		return fmt.Sprintf("flow %s {\n  step S1 targets selector { %s }\n}\n",
			name, strings.Join(anchors, "; "))
	}
	// Multi-line whitespace-separated form (the renderer's shape).
	return fmt.Sprintf("flow %s {\n  step S1 targets selector { %s }\n}\n",
		name, strings.Join(anchors, " "))
}

// TestProperty_SelectorRoundTrip asserts Parse(Render(parse(src))) ==
// Parse(src) for randomly-generated selectors. Equality is checked
// structurally on the AST (positions and *Lit identity excluded).
func TestProperty_SelectorRoundTrip(t *testing.T) {
	check := func(seed int64) bool {
		rng := rand.New(rand.NewSource(seed))
		src := genSelector(rng, "S")
		f1, err := ParseString("prop.gh", src)
		if err != nil {
			t.Logf("seed=%d src=%s parse err=%v", seed, src, err)
			return false
		}
		rendered := Render(f1)
		f2, err := ParseString("rendered.gh", rendered)
		if err != nil {
			t.Logf("seed=%d rendered=%s parse err=%v", seed, rendered, err)
			return false
		}
		return selectorASTEqual(f1.Decls[0].Selector, f2.Decls[0].Selector)
	}
	if err := quick.Check(check, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}

// TestProperty_InlineSelectorRoundTrip is the analogue for inline
// selectors inside flow steps.
func TestProperty_InlineSelectorRoundTrip(t *testing.T) {
	check := func(seed int64) bool {
		rng := rand.New(rand.NewSource(seed))
		src := genInlineSelectorFlow(rng, "F")
		f1, err := ParseString("prop.gh", src)
		if err != nil {
			t.Logf("seed=%d src=%s parse err=%v", seed, src, err)
			return false
		}
		rendered := Render(f1)
		f2, err := ParseString("rendered.gh", rendered)
		if err != nil {
			t.Logf("seed=%d rendered=%s parse err=%v", seed, rendered, err)
			return false
		}
		s1 := f1.Decls[0].Flow.Steps[0].Targets.InlineSelector
		s2 := f2.Decls[0].Flow.Steps[0].Targets.InlineSelector
		return inlineSelectorEqual(s1, s2)
	}
	if err := quick.Check(check, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}

// TestProperty_RenderIsIdempotent asserts Render(Parse(Render(Parse(s))))
// is byte-equal to Render(Parse(s)) — i.e. once a source has been
// normalized through the renderer, further round-trips are stable.
func TestProperty_RenderIsIdempotent(t *testing.T) {
	check := func(seed int64) bool {
		rng := rand.New(rand.NewSource(seed))
		src := genSelector(rng, "S") + genInlineSelectorFlow(rng, "F")
		f1, err := ParseString("prop.gh", src)
		if err != nil {
			t.Logf("seed=%d src=%s parse err=%v", seed, src, err)
			return false
		}
		r1 := Render(f1)
		f2, err := ParseString("r1.gh", r1)
		if err != nil {
			t.Logf("seed=%d r1=%s parse err=%v", seed, r1, err)
			return false
		}
		r2 := Render(f2)
		return r1 == r2
	}
	if err := quick.Check(check, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}

func selectorASTEqual(a, b *Selector) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Name != b.Name || a.Unique != b.Unique {
		return false
	}
	if len(a.Anchors) != len(b.Anchors) {
		return false
	}
	for i := range a.Anchors {
		if a.Anchors[i].Kind != b.Anchors[i].Kind {
			return false
		}
		if !litEqual(a.Anchors[i].Value, b.Anchors[i].Value) {
			return false
		}
	}
	return true
}

func inlineSelectorEqual(a, b *InlineSelector) bool {
	if a == nil || b == nil {
		return a == b
	}
	if len(a.Anchors) != len(b.Anchors) {
		return false
	}
	for i := range a.Anchors {
		if a.Anchors[i].Kind != b.Anchors[i].Kind {
			return false
		}
		if !litEqual(a.Anchors[i].Value, b.Anchors[i].Value) {
			return false
		}
	}
	return true
}
