package scip

import (
	"testing"

	"github.com/shivamstaq/graph-harness/internal/source_live"
	"github.com/shivamstaq/graph-harness/internal/source_live/scip/proto"
)

// scip-go encodes the full Go module path as a single namespace
// descriptor (e.g. `example.com/checkout/internal/checkout`/Validator#Validate().).
// goQualifyOverride must trim that descriptor to its leaf segment so
// the qualified name matches what tree-sitter and gopls produce. The
// §6.12 canonical-ID equivalence depends on this — without it,
// three-source unification falls apart for every Go entity.
func TestGoImporter_TrimsModulePathNamespace(t *testing.T) {
	idx := &proto.Index{
		Documents: []*proto.Document{
			{
				RelativePath: "internal/checkout/validator.go",
				Language:     "go",
				Symbols: []*proto.SymbolInformation{
					{
						Symbol:      "scip-go gomod example.com/checkout . `example.com/checkout/internal/checkout`/CheckoutValidator#",
						DisplayName: "CheckoutValidator",
						Kind:        proto.KindClass,
					},
					{
						Symbol:      "scip-go gomod example.com/checkout . `example.com/checkout/internal/checkout`/CheckoutValidator#Validate().",
						DisplayName: "Validate",
						Kind:        proto.KindMethod,
					},
					{
						Symbol:      "scip-go gomod example.com/checkout . `example.com/checkout/internal/checkout`/",
						DisplayName: "checkout",
						Kind:        proto.KindPackage,
					},
				},
			},
		},
	}

	syms := NewGoImporter().Import(idx, "")

	want := map[string]struct {
		qn   string
		recv string
		kind source_live.SymbolKind
	}{
		"CheckoutValidator": {qn: "checkout.CheckoutValidator", recv: "", kind: source_live.SymbolKindClass},
		"Validate":          {qn: "checkout.CheckoutValidator.Validate", recv: "checkout.CheckoutValidator", kind: source_live.SymbolKindMethod},
		"checkout":          {qn: "checkout", recv: "", kind: source_live.SymbolKindModule},
	}

	if len(syms) != len(want) {
		t.Fatalf("syms count: want %d, got %d (%+v)", len(want), len(syms), syms)
	}
	got := map[string]source_live.Symbol{}
	for _, s := range syms {
		got[s.Name] = s
	}
	for name, w := range want {
		s, ok := got[name]
		if !ok {
			t.Errorf("missing symbol %q", name)
			continue
		}
		if s.QualifiedName != w.qn {
			t.Errorf("[%s] QualifiedName: want %q, got %q", name, w.qn, s.QualifiedName)
		}
		if s.Receiver != w.recv {
			t.Errorf("[%s] Receiver: want %q, got %q", name, w.recv, s.Receiver)
		}
		if s.Kind != w.kind {
			t.Errorf("[%s] Kind: want %q, got %q", name, w.kind, s.Kind)
		}
	}
}

// goQualifyOverride should be a no-op for symbols without a module-path
// namespace (e.g. tests that hand-craft minimal descriptors for the
// generic importDocument path).
func TestGoQualifyOverride_NoOpForFlatNamespace(t *testing.T) {
	p := &parsedSymbol{
		descriptors: []descriptor{
			{name: "checkout", suffix: suffixNamespace},
			{name: "Validator", suffix: suffixType},
			{name: "Validate", suffix: suffixMethod},
		},
	}
	qn, recv, kind := goQualifyOverride(p, "Validate")
	if qn != "" || recv != "" || kind != "" {
		t.Fatalf("expected no-op, got qn=%q recv=%q kind=%q", qn, recv, kind)
	}
}
