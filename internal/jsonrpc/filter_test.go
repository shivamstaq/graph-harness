package jsonrpc

import (
	"encoding/json"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// TestParseFilter_Shorthand covers the pre-F20 layer / layer/kind
// shorthand, which must keep working for back-compat.
func TestParseFilter_Shorthand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in       string
		wantLay  []string
		wantKind []string
	}{
		{"", nil, nil},
		{"code.core", []string{"code.core"}, nil},
		{"code.core/FileChanged", []string{"code.core"}, []string{"FileChanged"}},
		{"kernel.subscriptions/SubscriberEvicted", []string{"kernel.subscriptions"}, []string{"SubscriberEvicted"}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			f, err := parseFilter(tc.in)
			if err != nil {
				t.Fatalf("parseFilter(%q): %v", tc.in, err)
			}
			if !stringSliceEqual(f.Layers, tc.wantLay) {
				t.Errorf("layers: got %v, want %v", f.Layers, tc.wantLay)
			}
			if !stringSliceEqual(f.Kinds, tc.wantKind) {
				t.Errorf("kinds: got %v, want %v", f.Kinds, tc.wantKind)
			}
		})
	}
}

// TestParseFilter_FullGrammar covers the Participle path: explicit
// predicate fields, AND composition, parens.
func TestParseFilter_FullGrammar(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in       string
		wantLay  []string
		wantKind []string
	}{
		{`layer = "code.core"`, []string{"code.core"}, nil},
		{`layer = "code.core" AND kind = "FileChanged"`, []string{"code.core"}, []string{"FileChanged"}},
		{`(layer = "code.core" AND kind = "FileChanged")`, []string{"code.core"}, []string{"FileChanged"}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			f, err := parseFilter(tc.in)
			if err != nil {
				t.Fatalf("parseFilter(%q): %v", tc.in, err)
			}
			if !stringSliceEqual(f.Layers, tc.wantLay) {
				t.Errorf("layers: got %v, want %v", f.Layers, tc.wantLay)
			}
			if !stringSliceEqual(f.Kinds, tc.wantKind) {
				t.Errorf("kinds: got %v, want %v", f.Kinds, tc.wantKind)
			}
		})
	}
}

// TestParseFilter_RejectedAtLowering documents the forward-compat
// parsing: OR/NOT/produced_by parse cleanly but error at the
// EventFilter lowering since the kernel-side struct cannot yet
// represent disjunction. The grammar accepts them so a future
// kernel.EventFilter-with-typed-predicates can switch the lowering
// without re-parsing the wire format.
func TestParseFilter_RejectedAtLowering(t *testing.T) {
	t.Parallel()
	cases := []string{
		`layer = "code.core" OR layer = "semantic.overlay"`,
		`NOT layer = "code.core"`,
		`produced_by = "extractor:lsp:go"`,
		`layer ~ "code"`, // ~ op also unsupported at lowering
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			_, err := parseFilter(in)
			if err == nil {
				t.Errorf("expected lowering error for %q, got nil", in)
			}
		})
	}
}

// TestParseFilter_MatchesShorthandSemantics confirms the lowering
// produces an EventFilter that matches the same events as before.
func TestParseFilter_MatchesShorthandSemantics(t *testing.T) {
	t.Parallel()
	ev := kernel.Event{Layer: "code.core", Kind: "FileChanged", Payload: json.RawMessage(`{}`)}
	miss := kernel.Event{Layer: "semantic.overlay", Kind: "OverlaySaved", Payload: json.RawMessage(`{}`)}

	for _, expr := range []string{
		"code.core",
		`layer = "code.core"`,
	} {
		f, err := parseFilter(expr)
		if err != nil {
			t.Fatalf("parseFilter(%q): %v", expr, err)
		}
		if !f.Matches(ev) {
			t.Errorf("filter %q should match %+v", expr, ev)
		}
		if f.Matches(miss) {
			t.Errorf("filter %q should NOT match %+v", expr, miss)
		}
	}
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, s := range a {
		if s != b[i] {
			return false
		}
	}
	return true
}
