package scip

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/shivamstaq/graph-harness/internal/source_live"
	"github.com/shivamstaq/graph-harness/internal/source_live/scip/proto"
)

// helpers for hand-encoding SCIP wire-format fixtures.

func encString(buf []byte, field int, s string) []byte {
	buf = protowire.AppendTag(buf, protowire.Number(field), protowire.BytesType) //nolint:gosec
	return protowire.AppendString(buf, s)
}

func encVarint(buf []byte, field int, v uint64) []byte {
	buf = protowire.AppendTag(buf, protowire.Number(field), protowire.VarintType) //nolint:gosec
	return protowire.AppendVarint(buf, v)
}

func encBytes(buf []byte, field int, body []byte) []byte {
	buf = protowire.AppendTag(buf, protowire.Number(field), protowire.BytesType) //nolint:gosec
	return protowire.AppendBytes(buf, body)
}

func encPackedInt32(buf []byte, field int, vals []int32) []byte {
	body := []byte{}
	for _, v := range vals {
		body = protowire.AppendVarint(body, uint64(int64(v))) //nolint:gosec
	}
	return encBytes(buf, field, body)
}

// buildIndex constructs a SCIP Index over a single Go document with
// one struct + one method, plus the matching definition occurrences.
func buildIndex(t *testing.T) []byte {
	t.Helper()
	// SymbolInformation 1: type Validator
	si1 := []byte{}
	si1 = encString(si1, 1, "scip-go gomod github.com/foo v1 `pkg/checkout`/Validator#")
	si1 = encVarint(si1, 5, uint64(proto.KindStruct))
	si1 = encString(si1, 6, "Validator")

	// SymbolInformation 2: method Validate
	si2 := []byte{}
	si2 = encString(si2, 1, "scip-go gomod github.com/foo v1 `pkg/checkout`/Validator#Validate().")
	si2 = encVarint(si2, 5, uint64(proto.KindMethod))
	si2 = encString(si2, 6, "Validate")

	// Definition occurrence for Validate at lines [3, 0, 5, 4]
	occ := []byte{}
	occ = encPackedInt32(occ, 1, []int32{3, 0, 5, 4})
	occ = encString(occ, 2, "scip-go gomod github.com/foo v1 `pkg/checkout`/Validator#Validate().")
	occ = encVarint(occ, 3, uint64(proto.RoleDefinition))

	// Document
	doc := []byte{}
	doc = encString(doc, 1, "pkg/checkout/validator.go")
	doc = encBytes(doc, 2, occ)
	doc = encBytes(doc, 3, si1)
	doc = encBytes(doc, 3, si2)
	doc = encString(doc, 4, "go")

	// Index.documents
	idx := []byte{}
	idx = encBytes(idx, 2, doc)
	return idx
}

func TestDecodeIndex_Document(t *testing.T) {
	body := buildIndex(t)
	idx, err := proto.DecodeIndex(body)
	if err != nil {
		t.Fatalf("DecodeIndex: %v", err)
	}
	if len(idx.Documents) != 1 {
		t.Fatalf("got %d documents, want 1", len(idx.Documents))
	}
	doc := idx.Documents[0]
	if doc.RelativePath != "pkg/checkout/validator.go" {
		t.Errorf("relative_path = %q", doc.RelativePath)
	}
	if doc.Language != "go" {
		t.Errorf("language = %q", doc.Language)
	}
	if len(doc.Symbols) != 2 {
		t.Errorf("got %d symbols, want 2", len(doc.Symbols))
	}
	if len(doc.Occurrences) != 1 {
		t.Errorf("got %d occurrences, want 1", len(doc.Occurrences))
	}
	occ := doc.Occurrences[0]
	if occ.SymbolRoles&proto.RoleDefinition == 0 {
		t.Errorf("expected definition role bit, got %d", occ.SymbolRoles)
	}
	if got := occ.Range; len(got) != 4 || got[0] != 3 || got[2] != 5 {
		t.Errorf("range = %v, want [3 0 5 4]", got)
	}
}

func TestImportGo_TranslatesSCIPSymbols(t *testing.T) {
	body := buildIndex(t)
	idx, err := proto.DecodeIndex(body)
	if err != nil {
		t.Fatalf("DecodeIndex: %v", err)
	}
	imp := NewGoImporter()
	syms := imp.Import(idx, "")
	if len(syms) != 2 {
		t.Fatalf("got %d symbols, want 2: %+v", len(syms), syms)
	}
	var validator, validate source_live.Symbol
	for _, s := range syms {
		switch s.QualifiedName {
		case "pkg/checkout.Validator":
			validator = s
		case "pkg/checkout.Validator.Validate":
			validate = s
		}
	}
	if validator.Kind != source_live.SymbolKindClass {
		t.Errorf("validator kind = %q", validator.Kind)
	}
	if validate.Kind != source_live.SymbolKindMethod {
		t.Errorf("validate kind = %q", validate.Kind)
	}
	if validate.Receiver != "pkg/checkout.Validator" {
		t.Errorf("validate receiver = %q", validate.Receiver)
	}
	if validate.SourceClass != source_live.SourceClassSCIP {
		t.Errorf("source class = %q", validate.SourceClass)
	}
	if validate.LanguageID != "go" {
		t.Errorf("language = %q", validate.LanguageID)
	}
	if validate.Range.StartLine != 4 {
		t.Errorf("validate start line = %d, want 4 (SCIP 0-based + 1)", validate.Range.StartLine)
	}
	if validate.Path != "pkg/checkout/validator.go" {
		t.Errorf("path = %q", validate.Path)
	}
	if validate.ProducedBy != "extractor:scip:scip-go" {
		t.Errorf("producedBy = %q", validate.ProducedBy)
	}
}

func TestImportGo_PathFilter(t *testing.T) {
	body := buildIndex(t)
	idx, _ := proto.DecodeIndex(body)
	imp := NewGoImporter()
	if got := imp.Import(idx, "other.go"); len(got) != 0 {
		t.Errorf("path filter mismatch should yield 0 symbols, got %d", len(got))
	}
	if got := imp.Import(idx, "pkg/checkout/validator.go"); len(got) != 2 {
		t.Errorf("path filter match should yield 2 symbols, got %d", len(got))
	}
}

func TestParseSymbol_Roundtrip(t *testing.T) {
	cases := []struct {
		in       string
		wantQN   string
		wantRecv string
		wantSuf  descriptorSuffix
	}{
		{
			in:       "scip-go gomod github.com/foo v1 `pkg/checkout`/Validator#Validate().",
			wantQN:   "pkg/checkout.Validator.Validate",
			wantRecv: "pkg/checkout.Validator",
			wantSuf:  suffixMethod,
		},
		{
			in:      "scip-typescript npm @foo/bar 1 `src/auth`/CheckoutValidator#",
			wantQN:  "src/auth.CheckoutValidator",
			wantSuf: suffixType,
		},
		{
			in:      "scip-python . . . sample/`util.py`/helper.",
			wantQN:  "sample.util.py.helper",
			wantSuf: suffixTerm,
		},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			p := parseSymbol(tc.in)
			if p == nil {
				t.Fatalf("parseSymbol returned nil")
			}
			if got := p.qualifiedName(); got != tc.wantQN {
				t.Errorf("qualifiedName = %q, want %q", got, tc.wantQN)
			}
			if got := p.methodReceiver(); got != tc.wantRecv {
				t.Errorf("receiver = %q, want %q", got, tc.wantRecv)
			}
			if got := p.terminalSuffix(); got != tc.wantSuf {
				t.Errorf("terminal suffix = %q, want %q", got, tc.wantSuf)
			}
		})
	}
}

func TestParseSymbol_Local(t *testing.T) {
	p := parseSymbol("local 42")
	if p == nil || p.local != "42" {
		t.Errorf("local symbol parse = %+v", p)
	}
}

func TestRead_ReturnsErrorOnMissingFile(t *testing.T) {
	_, err := Read(filepath.Join(t.TempDir(), "missing.scip"))
	if err == nil {
		t.Errorf("expected error reading missing file")
	}
}

func TestRead_ParsesFixtureFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.scip")
	if err := os.WriteFile(path, buildIndex(t), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	idx, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(idx.Documents) != 1 {
		t.Errorf("got %d docs", len(idx.Documents))
	}
}

func TestFindIndexes_MissingDirReturnsEmpty(t *testing.T) {
	tmp := t.TempDir()
	got, err := FindIndexes(tmp)
	if err != nil {
		t.Fatalf("FindIndexes: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d indexes from empty workspace", len(got))
	}
}

func TestFindIndexes_FindsScipFiles(t *testing.T) {
	tmp := t.TempDir()
	idxDir := filepath.Join(tmp, ".scip-index")
	if err := os.MkdirAll(idxDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, n := range []string{"a.scip", "b.scip", "c.txt"} {
		if err := os.WriteFile(filepath.Join(idxDir, n), []byte{}, 0o600); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}
	got, err := FindIndexes(tmp)
	if err != nil {
		t.Fatalf("FindIndexes: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 .scip files, got %d: %v", len(got), got)
	}
}
