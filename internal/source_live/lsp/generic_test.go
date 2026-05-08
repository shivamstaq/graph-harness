package lsp

import (
	"encoding/json"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/source_live"
)

func TestFlattenDocumentSymbols(t *testing.T) {
	d := newGenericDriver(driverConf{
		languageID: "go",
		producedBy: "extractor:lsp:gopls",
		qualify:    goQualify,
	})
	syms := []documentSymbol{
		{
			Name:           "Validator",
			Kind:           lspKindStruct,
			Range:          rangeT{Start: position{Line: 0}, End: position{Line: 5}},
			SelectionRange: rangeT{Start: position{Line: 0}, End: position{Line: 0, Character: 9}},
			Children: []documentSymbol{
				{
					Name:           "Validate",
					Detail:         "(cart) error",
					Kind:           lspKindMethod,
					Range:          rangeT{Start: position{Line: 1}, End: position{Line: 4}},
					SelectionRange: rangeT{Start: position{Line: 1}, End: position{Line: 1, Character: 8}},
				},
			},
		},
		{
			Name:           "Helper",
			Detail:         "() string",
			Kind:           lspKindFunction,
			Range:          rangeT{Start: position{Line: 7}, End: position{Line: 9}},
			SelectionRange: rangeT{Start: position{Line: 7}, End: position{Line: 7, Character: 6}},
		},
	}
	out := d.flattenDocumentSymbols(syms, "validator.go", nil)
	if len(out) != 3 {
		t.Fatalf("flatten produced %d symbols, want 3: %+v", len(out), out)
	}
	wantKinds := []source_live.SymbolKind{
		source_live.SymbolKindClass, source_live.SymbolKindMethod, source_live.SymbolKindFunction,
	}
	for i, s := range out {
		if s.Kind != wantKinds[i] {
			t.Errorf("symbol[%d].Kind = %q, want %q", i, s.Kind, wantKinds[i])
		}
		if s.SourceClass != source_live.SourceClassLSP {
			t.Errorf("symbol[%d].SourceClass = %q, want %q", i, s.SourceClass, source_live.SourceClassLSP)
		}
		if s.LanguageID != "go" {
			t.Errorf("symbol[%d].LanguageID = %q, want go", i, s.LanguageID)
		}
		if s.Path != "validator.go" {
			t.Errorf("symbol[%d].Path = %q, want validator.go", i, s.Path)
		}
		if s.ProducedBy != "extractor:lsp:gopls" {
			t.Errorf("symbol[%d].ProducedBy = %q", i, s.ProducedBy)
		}
	}
	method := out[1]
	if method.QualifiedName != "Validator.Validate" {
		t.Errorf("method qn = %q, want Validator.Validate", method.QualifiedName)
	}
	if method.Receiver != "Validator" {
		t.Errorf("method receiver = %q, want Validator", method.Receiver)
	}
	if method.Signature != "(cart) error" {
		t.Errorf("method signature = %q, want (cart) error", method.Signature)
	}
}

func TestFlattenSymbolInformation_Legacy(t *testing.T) {
	d := newGenericDriver(driverConf{
		languageID: "typescript",
		producedBy: "extractor:lsp:tsserver",
		kindMap:    tsKindMap,
	})
	flat := []symbolInformation{
		{
			Name:          "validate",
			Kind:          lspKindMethod,
			ContainerName: "CheckoutValidator",
			Location: location{
				URI:   "file:///root/validator.ts",
				Range: rangeT{Start: position{Line: 1}, End: position{Line: 3}},
			},
		},
	}
	out := d.flattenSymbolInformation(flat, "validator.ts")
	if len(out) != 1 {
		t.Fatalf("got %d symbols, want 1", len(out))
	}
	got := out[0]
	if got.QualifiedName != "CheckoutValidator.validate" {
		t.Errorf("qn = %q", got.QualifiedName)
	}
	if got.Receiver != "CheckoutValidator" {
		t.Errorf("receiver = %q", got.Receiver)
	}
	if got.Kind != source_live.SymbolKindMethod {
		t.Errorf("kind = %q", got.Kind)
	}
}

func TestPathToURIRoundTrip(t *testing.T) {
	uri := pathToURI("/tmp/foo/bar.go")
	if uri != "file:///tmp/foo/bar.go" {
		t.Errorf("pathToURI = %q", uri)
	}
	rel := uriToRelPath("file:///root/sub/x.go", "/root")
	if rel != "sub/x.go" {
		t.Errorf("uriToRelPath = %q", rel)
	}
}

func TestGoplsDriverConfShape(t *testing.T) {
	d, ok := NewGoplsDriver().(*genericDriver)
	if !ok {
		t.Fatalf("NewGoplsDriver should return *genericDriver")
	}
	if d.conf.languageID != "go" {
		t.Errorf("languageID = %q", d.conf.languageID)
	}
	if d.conf.executable != "gopls" {
		t.Errorf("executable = %q", d.conf.executable)
	}
}

// TestUnmarshalLocations exercises the shared Definition/References
// projection without requiring a live LSP server.
func TestUnmarshalLocations(t *testing.T) {
	d := newGenericDriver(driverConf{languageID: "go"})
	d.root = "/root"
	raw, _ := json.Marshal([]location{
		{URI: "file:///root/sub/x.go", Range: rangeT{Start: position{Line: 1}, End: position{Line: 1, Character: 5}}},
	})
	out := d.unmarshalLocations(raw)
	if len(out) != 1 {
		t.Fatalf("got %d locations", len(out))
	}
	if out[0].Path != "sub/x.go" {
		t.Errorf("path = %q", out[0].Path)
	}
	if out[0].Range.StartLine != 2 {
		t.Errorf("start line = %d, want 2 (LSP zero-based +1)", out[0].Range.StartLine)
	}
}
