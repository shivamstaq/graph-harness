package code_core

import (
	"context"
	"fmt"

	"github.com/shivamstaq/graph-harness/internal/code_core/normalize"
	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// IngestParsedFile turns a [source_live.ParsedFile] into code.core
// entities + provenance. It is the tree-sitter-only ingestion path:
// every entity it materializes is recorded with a single
// `structural_treesitter` provenance entry. When the LSP and SCIP
// extractors land, callers that route all three into [Unifier.Unify]
// get the full three-source merge; this function remains the cheap
// path used by the watcher / parse-on-edit loop.
//
// Returns the materialized entity IDs in order (File first, then
// each Function/Method in declaration order).
func (s *Store) IngestParsedFile(ctx context.Context, pf *source_live.ParsedFile, createdSeq uint64) ([]string, error) {
	if pf == nil {
		return nil, nil
	}
	out := make([]string, 0, 1+len(pf.Functions))

	// File entity.
	fileEnt := Entity{
		ID:            FileID(pf.Path),
		Kind:          KindFile,
		LanguageID:    pf.Language,
		QualifiedName: pf.Path,
		Path:          pf.Path,
	}
	if err := s.PutEntity(ctx, fileEnt, createdSeq); err != nil {
		return nil, fmt.Errorf("put file entity: %w", err)
	}
	if err := s.UpsertProvenance(ctx, fileEnt.ID, treesitterEntry(createdSeq)); err != nil {
		return nil, fmt.Errorf("provenance file entity: %w", err)
	}
	out = append(out, fileEnt.ID)

	for _, fn := range pf.Functions {
		ns := normalize.ForLanguage(pf.Language, fn.Signature)
		var ent Entity
		if fn.Receiver == "" {
			ent = Entity{
				ID:            FunctionID(pf.Language, fn.QualifiedName, ns),
				Kind:          KindFunction,
				LanguageID:    pf.Language,
				QualifiedName: fn.QualifiedName,
				Path:          pf.Path,
				BodyHash:      fn.BodyHash,
			}
		} else {
			ent = Entity{
				ID:            MethodID(pf.Language, fn.Receiver, fn.Name, ns),
				Kind:          KindMethod,
				LanguageID:    pf.Language,
				QualifiedName: fn.QualifiedName,
				Receiver:      fn.Receiver,
				Path:          pf.Path,
				BodyHash:      fn.BodyHash,
			}
		}
		if err := s.PutEntity(ctx, ent, createdSeq); err != nil {
			return nil, fmt.Errorf("put function entity %s: %w", fn.QualifiedName, err)
		}
		if err := s.UpsertProvenance(ctx, ent.ID, treesitterEntry(createdSeq)); err != nil {
			return nil, fmt.Errorf("provenance function entity %s: %w", fn.QualifiedName, err)
		}
		out = append(out, ent.ID)
	}
	return out, nil
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
