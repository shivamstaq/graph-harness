package source_live

import "testing"

func TestSymbol_IsAnonymous(t *testing.T) {
	cases := []struct {
		name string
		s    Symbol
		want bool
	}{
		{
			name: "named function is not anonymous",
			s: Symbol{
				QualifiedName: "checkout.Validate",
				Kind:          SymbolKindFunction,
			},
			want: false,
		},
		{
			name: "closure with parent + no qualified name is anonymous",
			s: Symbol{
				Kind:     SymbolKindClosure,
				ParentID: "abc123",
				Ordinal:  1,
			},
			want: true,
		},
		{
			name: "missing parent → not anonymous (extractor bug)",
			s: Symbol{
				Kind: SymbolKindClosure,
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.IsAnonymous(); got != tc.want {
				t.Fatalf("IsAnonymous() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSymbol_KindTag(t *testing.T) {
	got := Symbol{Kind: SymbolKindMethod}.KindTag()
	if got != "Method" {
		t.Fatalf("KindTag() = %q, want %q", got, "Method")
	}
}

// TestSourceClassValues pins the wire-stable string values. Changing
// these is a coordinated migration across source.live, code.core, and
// the manifests, so tests should fail loudly if anyone tries to rename
// a constant in a one-off refactor.
func TestSourceClassValues(t *testing.T) {
	cases := map[SourceClass]string{
		SourceClassLSP:        "live_lsp",
		SourceClassSCIP:       "index_scip",
		SourceClassTreesitter: "structural_treesitter",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("SourceClass %q != %q", got, want)
		}
	}
}

// TestSymbolKindValues pins the canonical kind strings used in
// identity hashing (§6.12) and on-disk provenance records.
func TestSymbolKindValues(t *testing.T) {
	cases := map[SymbolKind]string{
		SymbolKindFile:      "File",
		SymbolKindModule:    "Module",
		SymbolKindFunction:  "Function",
		SymbolKindMethod:    "Method",
		SymbolKindTypeDecl:  "TypeDecl",
		SymbolKindClass:     "Class",
		SymbolKindInterface: "Interface",
		SymbolKindClosure:   "Closure",
		SymbolKindSymbol:    "Symbol",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("SymbolKind %q != %q", got, want)
		}
	}
}
