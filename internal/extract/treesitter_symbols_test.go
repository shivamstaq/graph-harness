package extract

import (
	"testing"

	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// TestParsedFileToSymbols_ProjectsClassAndInterface confirms that the
// tree-sitter TypeDecl pipeline reaches the Symbol envelope with the
// matching SymbolKind. Regressions here surface as a missing Class /
// Interface entity in code.core after unification (see
// code_core.TestUnifier_ProjectsClassAndInterfaceSymbols for the
// downstream half of the round trip).
func TestParsedFileToSymbols_ProjectsClassAndInterface(t *testing.T) {
	pf := &source_live.ParsedFile{
		Path:     "shapes.ts",
		Language: "typescript",
		TypeDecls: []source_live.TypeDeclDecl{
			{
				QualifiedName: "shapes.Foo",
				Name:          "Foo",
				Kind:          source_live.TypeDeclKindClass,
				StartByte:     0, EndByte: 20, StartLine: 1, EndLine: 3,
				BodyHash: "class-body-hash",
			},
			{
				QualifiedName: "shapes.Baz",
				Name:          "Baz",
				Kind:          source_live.TypeDeclKindInterface,
				StartByte:     21, EndByte: 60, StartLine: 4, EndLine: 6,
				BodyHash: "iface-body-hash",
			},
		},
	}
	syms := ParsedFileToSymbols(pf)
	wantKinds := map[string]source_live.SymbolKind{
		"shapes.Foo": source_live.SymbolKindClass,
		"shapes.Baz": source_live.SymbolKindInterface,
	}
	for _, s := range syms {
		want, ok := wantKinds[s.QualifiedName]
		if !ok {
			continue
		}
		if s.Kind != want {
			t.Errorf("symbol %s kind = %q, want %q", s.QualifiedName, s.Kind, want)
		}
		if s.LanguageID != "typescript" {
			t.Errorf("symbol %s language = %q, want typescript", s.QualifiedName, s.LanguageID)
		}
		if s.SourceClass != source_live.SourceClassTreesitter {
			t.Errorf("symbol %s source_class = %q, want %q",
				s.QualifiedName, s.SourceClass, source_live.SourceClassTreesitter)
		}
		delete(wantKinds, s.QualifiedName)
	}
	for qn := range wantKinds {
		t.Errorf("missing projected symbol %q", qn)
	}
}
