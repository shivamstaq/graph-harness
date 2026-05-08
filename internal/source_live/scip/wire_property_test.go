package scip

import (
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	upstream "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"

	scipproto "github.com/shivamstaq/graph-harness/internal/source_live/scip/proto"
)

// updateFixture, when set, regenerates the static fixture file.
// Run with `go test ./internal/source_live/scip/ -run TestWireFormat -update-fixture`.
var updateFixture = flag.Bool("update-fixture", false, "regenerate tests/testdata/scip/sample.scip")

// upstreamFixture builds an *upstream.Index that exercises every
// SymbolInformation / Occurrence shape our reader handles. The shape
// is deliberately chosen to overlap with the per-language importers'
// expectations (Go method on a struct, plus a free function).
func upstreamFixture() *upstream.Index {
	return &upstream.Index{
		Metadata: &upstream.Metadata{
			Version:              upstream.ProtocolVersion_UnspecifiedProtocolVersion,
			ProjectRoot:          "file:///workspace/sample",
			ToolInfo:             &upstream.ToolInfo{Name: "scip-go", Version: "0.1.0"},
			TextDocumentEncoding: upstream.TextEncoding_UTF8,
		},
		Documents: []*upstream.Document{
			{
				Language:     "go",
				RelativePath: "pkg/checkout/validator.go",
				Symbols: []*upstream.SymbolInformation{
					{
						Symbol:      "scip-go gomod github.com/foo v1 `pkg/checkout`/Validator#",
						Kind:        upstream.SymbolInformation_Struct,
						DisplayName: "Validator",
					},
					{
						Symbol:      "scip-go gomod github.com/foo v1 `pkg/checkout`/Validator#Validate().",
						Kind:        upstream.SymbolInformation_Method,
						DisplayName: "Validate",
					},
					{
						Symbol:      "scip-go gomod github.com/foo v1 `pkg/checkout`/Helper().",
						Kind:        upstream.SymbolInformation_Function,
						DisplayName: "Helper",
					},
				},
				Occurrences: []*upstream.Occurrence{
					{
						Range:       []int32{3, 0, 5, 4},
						Symbol:      "scip-go gomod github.com/foo v1 `pkg/checkout`/Validator#Validate().",
						SymbolRoles: int32(upstream.SymbolRole_Definition),
					},
					{
						Range:       []int32{8, 0, 10, 4},
						Symbol:      "scip-go gomod github.com/foo v1 `pkg/checkout`/Helper().",
						SymbolRoles: int32(upstream.SymbolRole_Definition),
					},
				},
			},
		},
	}
}

// TestWireFormat_UpstreamEncoderCrossDecodes is the wire-format
// property test stipulation (b) calls for: encode an Index via the
// upstream third-party encoder, decode it via our hand-rolled
// reader, and assert the round-trip preserves every field we read.
//
// If this test fails after a `go get -u` of github.com/scip-code/scip,
// the upstream wire format has drifted and our reader needs to be
// reconciled before the SCIP integration can ship. The test is
// deliberately the canary §6.17 calls for.
func TestWireFormat_UpstreamEncoderCrossDecodes(t *testing.T) {
	idx := upstreamFixture()
	wire, err := proto.Marshal(idx)
	if err != nil {
		t.Fatalf("upstream Marshal: %v", err)
	}
	got, err := scipproto.DecodeIndex(wire)
	if err != nil {
		t.Fatalf("our DecodeIndex: %v", err)
	}
	if len(got.Documents) != 1 {
		t.Fatalf("docs = %d, want 1", len(got.Documents))
	}
	doc := got.Documents[0]
	if doc.RelativePath != "pkg/checkout/validator.go" {
		t.Errorf("relative_path = %q", doc.RelativePath)
	}
	if doc.Language != "go" {
		t.Errorf("language = %q", doc.Language)
	}
	if len(doc.Symbols) != 3 {
		t.Errorf("symbols = %d, want 3", len(doc.Symbols))
	}
	if len(doc.Occurrences) != 2 {
		t.Errorf("occurrences = %d, want 2", len(doc.Occurrences))
	}
	// Spot-check the method occurrence: 4-element packed range round-tripped.
	var methodOcc *scipproto.Occurrence
	for _, occ := range doc.Occurrences {
		if occ.Symbol == "scip-go gomod github.com/foo v1 `pkg/checkout`/Validator#Validate()." {
			methodOcc = occ
			break
		}
	}
	if methodOcc == nil {
		t.Fatalf("missing method occurrence in decoded doc")
	}
	if methodOcc.SymbolRoles&scipproto.RoleDefinition == 0 {
		t.Errorf("method occurrence missing Definition role bit")
	}
	if r := methodOcc.Range; len(r) != 4 || r[0] != 3 || r[1] != 0 || r[2] != 5 || r[3] != 4 {
		t.Errorf("range = %v, want [3 0 5 4]", r)
	}

	// Run the per-language importer against the decoded index — the
	// canary is end-to-end: upstream encoder → our decoder → our
	// importer → []source_live.Symbol.
	imp := NewGoImporter()
	syms := imp.Import(got, "")
	if len(syms) != 3 {
		t.Fatalf("import emitted %d symbols, want 3: %+v", len(syms), syms)
	}
	wantQNs := map[string]bool{
		"pkg/checkout.Validator":          true,
		"pkg/checkout.Validator.Validate": true,
		"pkg/checkout.Helper":             true,
	}
	for _, s := range syms {
		if !wantQNs[s.QualifiedName] {
			t.Errorf("unexpected qualified name %q", s.QualifiedName)
			continue
		}
		delete(wantQNs, s.QualifiedName)
	}
	for qn := range wantQNs {
		t.Errorf("missing qualified name %q in importer output", qn)
	}
}

// TestWireFormat_StaticFixtureCrossDecodes decodes the committed
// `tests/testdata/scip/sample.scip` blob (regenerated via
// `-update-fixture`) and asserts the same shape as the live property
// test. This catches drift even without re-running the upstream
// encoder — useful in environments where the test-only dep can't be
// downloaded (offline CI, restricted networks).
func TestWireFormat_StaticFixtureCrossDecodes(t *testing.T) {
	path := fixturePath(t)
	if *updateFixture {
		regenerateFixture(t, path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	got, err := scipproto.DecodeIndex(body)
	if err != nil {
		t.Fatalf("DecodeIndex: %v", err)
	}
	if len(got.Documents) != 1 {
		t.Fatalf("docs = %d, want 1", len(got.Documents))
	}
	if got.Documents[0].RelativePath != "pkg/checkout/validator.go" {
		t.Errorf("relative_path = %q", got.Documents[0].RelativePath)
	}
	if len(got.Documents[0].Symbols) != 3 {
		t.Errorf("symbols = %d, want 3", len(got.Documents[0].Symbols))
	}
}

// fixturePath returns the absolute path to tests/testdata/scip/sample.scip
// regardless of where the test binary is invoked from.
func fixturePath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	// thisFile = .../internal/source_live/scip/wire_property_test.go
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	return filepath.Join(repoRoot, "tests", "testdata", "scip", "sample.scip")
}

// regenerateFixture marshals the upstreamFixture() value and writes it
// to path. Used via `go test -run TestWireFormat -update-fixture` when
// the upstream schema changes shape and the static blob needs refresh.
func regenerateFixture(t *testing.T, path string) {
	t.Helper()
	idx := upstreamFixture()
	body, err := proto.Marshal(idx)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Logf("regenerated fixture: %s (%d bytes)", path, len(body))
}
