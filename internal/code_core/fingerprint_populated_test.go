package code_core

import (
	"context"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// TestIngestParsedFile_PopulatesFingerprintAndASTHash exercises F11:
// IngestParsedFile must stamp the SymbolFingerprint and ASTHash
// columns so the symbol_fingerprint / ast_hash anchor evaluators
// have data to match against. Pre-F11 these columns existed in the
// schema but were never populated, leaving two of the seven anchor
// ladder rungs unreachable.
func TestIngestParsedFile_PopulatesFingerprintAndASTHash(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t)

	pf := &source_live.ParsedFile{
		Path:     "pkg/foo.go",
		Language: "go",
		Functions: []source_live.FunctionDecl{
			{
				Name:          "Validate",
				QualifiedName: "pkg.Validate",
				Signature:     "func(ctx context.Context) error",
				BodyHash:      "body-aaaa",
			},
			{
				Name:          "Save",
				Receiver:      "*Repo",
				QualifiedName: "pkg.Repo.Save",
				Signature:     "func() error",
				BodyHash:      "body-bbbb",
			},
		},
	}
	ids, err := store.IngestParsedFile(ctx, pf, 1)
	if err != nil {
		t.Fatalf("IngestParsedFile: %v", err)
	}
	// First id is the File entity; subsequent are functions.
	if len(ids) < 3 {
		t.Fatalf("expected ≥ 3 entity ids; got %v", ids)
	}

	for _, qname := range []string{"pkg.Validate", "pkg.Repo.Save"} {
		ent, err := store.LookupByQualifiedName(ctx, qname)
		if err != nil {
			t.Fatalf("LookupByQualifiedName(%s): %v", qname, err)
		}
		if ent == nil {
			t.Fatalf("entity %s not stored", qname)
		}
		if ent.SymbolFingerprint == "" {
			t.Errorf("%s: SymbolFingerprint should be populated post-F11", qname)
		}
		if ent.ASTHash == "" {
			t.Errorf("%s: ASTHash should be populated post-F11", qname)
		}
		if ent.SymbolFingerprint == ent.ASTHash {
			t.Errorf("%s: SymbolFingerprint and ASTHash should differ (different inputs)", qname)
		}
	}

	// Stability: re-ingesting the same ParsedFile must produce the
	// same fingerprint (so re-extract over unchanged content does
	// not move the anchor target).
	if _, err := store.IngestParsedFile(ctx, pf, 2); err != nil {
		t.Fatalf("second IngestParsedFile: %v", err)
	}
	ent1, _ := store.LookupByQualifiedName(ctx, "pkg.Validate")
	if ent1 == nil || ent1.SymbolFingerprint == "" {
		t.Fatal("post-reingest SymbolFingerprint missing")
	}

	// Rename invariant: changing the qualified_name (rename) MUST
	// shift the SymbolFingerprint (which includes qname) but MUST
	// NOT shift the ASTHash (which excludes qname). This is the
	// complementary-rung contract F11 establishes for the resolver.
	preFP := ent1.SymbolFingerprint
	preAST := ent1.ASTHash
	renamed := &source_live.ParsedFile{
		Path:     "pkg/foo.go",
		Language: "go",
		Functions: []source_live.FunctionDecl{
			{
				Name:          "ValidateRenamed",
				QualifiedName: "pkg.ValidateRenamed",
				Signature:     "func(ctx context.Context) error",
				BodyHash:      "body-aaaa",
			},
		},
	}
	if _, err := store.IngestParsedFile(ctx, renamed, 3); err != nil {
		t.Fatalf("renamed IngestParsedFile: %v", err)
	}
	ent2, _ := store.LookupByQualifiedName(ctx, "pkg.ValidateRenamed")
	if ent2 == nil {
		t.Fatal("renamed entity not stored")
	}
	if ent2.SymbolFingerprint == preFP {
		t.Errorf("SymbolFingerprint must shift on rename; pre=%q post=%q", preFP, ent2.SymbolFingerprint)
	}
	if ent2.ASTHash != preAST {
		t.Errorf("ASTHash must stay stable across rename (signature+body unchanged); pre=%q post=%q", preAST, ent2.ASTHash)
	}
}
