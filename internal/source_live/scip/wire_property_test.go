package scip

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	scipproto "github.com/shivamstaq/graph-harness/internal/source_live/scip/proto"
)

// TestWireFormat_StaticFixtureCrossDecodes is the wire-format canary
// stipulation (b) calls for: decode the canonical `sample.scip` blob
// (produced once by the upstream Sourcegraph SCIP Go encoder via the
// build-tag-gated tool at `internal/source_live/scip/cmd/genfixture/`)
// through our hand-rolled reader and assert every field source.live
// consumes survives the round-trip.
//
// Production code never imports the upstream encoder; the encoder runs
// only when a maintainer regenerates the blob via:
//
//	go run -tags genscipfixture ./internal/source_live/scip/cmd/genfixture
//
// If this test fails after a regeneration, the upstream wire format
// has drifted and our reader needs reconciling before the SCIP
// integration can ship — the §6.17 "Build-our-own" canary.
func TestWireFormat_StaticFixtureCrossDecodes(t *testing.T) {
	body, err := os.ReadFile(fixturePath(t))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got, err := scipproto.DecodeIndex(body)
	if err != nil {
		t.Fatalf("DecodeIndex: %v", err)
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
	// Spot-check: 4-element packed range survived the upstream encoder.
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

	// End-to-end: upstream encoder → static blob → our decoder → our
	// importer. Validates the whole P1.B path produces the SymbolKind +
	// Receiver + QualifiedName three-source unification needs.
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

// fixturePath returns the absolute path to tests/testdata/scip/sample.scip
// regardless of where the test binary is invoked from.
func fixturePath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	return filepath.Join(repoRoot, "tests", "testdata", "scip", "sample.scip")
}
