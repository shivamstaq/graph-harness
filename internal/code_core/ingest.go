package code_core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/shivamstaq/graph-harness/internal/code_core/normalize"
	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// computeSymbolFingerprintLocal mirrors the helper in extract/treesitter_symbols.go.
// We define it locally here so the code_core batch ingestion path
// doesn't depend on internal/extract (which depends on code_core —
// the inverse would create an import cycle). F11.
func computeSymbolFingerprintLocal(language, qname, receiver, signature string) string {
	ns := normalize.ForLanguage(language, signature)
	h := sha256.New()
	for _, s := range []string{language, qname, receiver, ns} {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// computeASTHashLocal mirrors extract/treesitter_symbols.go::computeASTHash.
func computeASTHashLocal(language, receiver, signature, bodyHash string) string {
	ns := normalize.ForLanguage(language, signature)
	h := sha256.New()
	for _, s := range []string{language, receiver, ns, bodyHash} {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// IngestParsedFile turns a [source_live.ParsedFile] into code.core
// entities + provenance. It is the tree-sitter-only ingestion path:
// every entity it materializes is recorded with a single
// `structural_treesitter` provenance entry. When the LSP and SCIP
// extractors land, callers that route all three into [Unifier.Unify]
// get the full three-source merge; this function remains the cheap
// path used by the watcher / parse-on-edit loop.
//
// Returns the materialized entity IDs in order (File first, then
// each Function/Method in declaration order). For the change-aware
// variant used by the watcher → orchestrator path (SPEC §6.21
// compare-before-emit), see IngestParsedFileWithChange.
func (s *Store) IngestParsedFile(ctx context.Context, pf *source_live.ParsedFile, createdSeq uint64) ([]string, error) {
	ids, _, err := s.IngestParsedFileWithChange(ctx, pf, createdSeq)
	return ids, err
}

// IngestParsedFileWithChange is IngestParsedFile with an explicit
// changed-or-not signal. Watcher-driven re-extracts that observe an
// unchanged file produce changed=false; the daemon orchestrator
// skips drift-event emission accordingly. Returns the materialized
// entity IDs and the aggregate change flag (OR across every entity
// touched).
func (s *Store) IngestParsedFileWithChange(ctx context.Context, pf *source_live.ParsedFile, createdSeq uint64) ([]string, bool, error) {
	if pf == nil {
		return nil, false, nil
	}
	out := make([]string, 0, 1+len(pf.Functions))
	anyChanged := false

	// File entity.
	fileEnt := Entity{
		ID:            FileID(pf.Path),
		Kind:          KindFile,
		LanguageID:    pf.Language,
		QualifiedName: pf.Path,
		Path:          pf.Path,
	}
	entChanged, err := s.PutEntityIfChanged(ctx, fileEnt, createdSeq)
	if err != nil {
		return nil, false, fmt.Errorf("put file entity: %w", err)
	}
	provChanged, err := s.UpsertProvenanceIfChanged(ctx, fileEnt.ID, treesitterEntry(createdSeq))
	if err != nil {
		return nil, false, fmt.Errorf("provenance file entity: %w", err)
	}
	anyChanged = anyChanged || entChanged || provChanged
	out = append(out, fileEnt.ID)

	for _, fn := range pf.Functions {
		ns := normalize.ForLanguage(pf.Language, fn.Signature)
		// F11: SymbolFingerprint + ASTHash mirror what the watcher
		// path computes via extract.ParsedFileToSymbols. Defining
		// them locally here so the batch ingestion path doesn't
		// depend on the extract package (would invert the layering).
		fp := computeSymbolFingerprintLocal(pf.Language, fn.QualifiedName, fn.Receiver, fn.Signature)
		ah := computeASTHashLocal(pf.Language, fn.Receiver, fn.Signature, fn.BodyHash)
		var ent Entity
		if fn.Receiver == "" {
			ent = Entity{
				ID:                  FunctionID(pf.Language, fn.QualifiedName, ns),
				Kind:                KindFunction,
				LanguageID:          pf.Language,
				QualifiedName:       fn.QualifiedName,
				Path:                pf.Path,
				BodyHash:            fn.BodyHash,
				NormalizedSignature: ns,
				SymbolFingerprint:   fp,
				ASTHash:             ah,
			}
		} else {
			ent = Entity{
				ID:                  MethodID(pf.Language, fn.Receiver, fn.Name, ns),
				Kind:                KindMethod,
				LanguageID:          pf.Language,
				QualifiedName:       fn.QualifiedName,
				Receiver:            fn.Receiver,
				Path:                pf.Path,
				BodyHash:            fn.BodyHash,
				NormalizedSignature: ns,
				SymbolFingerprint:   fp,
				ASTHash:             ah,
			}
		}
		entChanged, err := s.PutEntityIfChanged(ctx, ent, createdSeq)
		if err != nil {
			return nil, false, fmt.Errorf("put function entity %s: %w", fn.QualifiedName, err)
		}
		provChanged, err := s.UpsertProvenanceIfChanged(ctx, ent.ID, treesitterEntry(createdSeq))
		if err != nil {
			return nil, false, fmt.Errorf("provenance function entity %s: %w", fn.QualifiedName, err)
		}
		anyChanged = anyChanged || entChanged || provChanged
		out = append(out, ent.ID)
	}
	return out, anyChanged, nil
}

// treesitterEntry returns the canonical SourceEntry for a tree-sitter
// observation at seq. Centralized here so the parse-on-edit loop and
// the test harness use identical defaults.
func treesitterEntry(seq uint64) SourceEntry {
	return SourceEntry{
		SourceClass: SourceClassTreesitter,
		Confidence:  1.0,
		LastSeenSeq: seq,
		Freshness:   FreshnessLive,
		ProducedBy:  "extractor:treesitter",
	}
}
