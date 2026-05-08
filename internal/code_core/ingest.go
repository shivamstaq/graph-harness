package code_core

import (
	"context"
	"fmt"

	"github.com/shivamstaq/graph-harness/internal/code_core/normalize"
	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// IngestParsedFile turns a source_live.ParsedFile into code.core entities and
// stores them via the SQLite adapter. Calls and references relations are
// not extracted in P0 (they require call-graph analysis beyond the structural
// grammar). When LSP/SCIP join in P1, the call edges populate.
//
// Returns the materialized entity IDs in order.
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
				BodyHash:      fn.BodyHash,
			}
		} else {
			ent = Entity{
				ID:            MethodID(pf.Language, fn.Receiver, fn.Name, ns),
				Kind:          KindMethod,
				LanguageID:    pf.Language,
				QualifiedName: fn.QualifiedName,
				Receiver:      fn.Receiver,
				BodyHash:      fn.BodyHash,
			}
		}
		if err := s.PutEntity(ctx, ent, createdSeq); err != nil {
			return nil, fmt.Errorf("put function entity %s: %w", fn.QualifiedName, err)
		}
		out = append(out, ent.ID)
	}
	return out, nil
}
