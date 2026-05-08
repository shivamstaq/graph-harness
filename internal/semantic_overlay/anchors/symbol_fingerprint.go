package anchors

import (
	"context"
	"fmt"

	"github.com/shivamstaq/graph-harness/internal/dsl"
)

// SymbolFingerprint matches on `code.core.symbol_fingerprint` exact
// equality. Extractors that emit a fingerprint (hash of declaration shape
// agnostic to the surrounding name) populate the column; evaluators degrade
// gracefully when the column is empty.
type SymbolFingerprint struct{}

// Kind returns "symbol_fingerprint".
func (SymbolFingerprint) Kind() string { return "symbol_fingerprint" }

// Evaluate returns every entity with the requested fingerprint.
func (SymbolFingerprint) Evaluate(ctx context.Context, a *dsl.Anchor, store Lookup) ([]Match, error) {
	fp := stringValue(a)
	if fp == "" {
		return nil, nil
	}
	ents, err := store.LookupBySymbolFingerprint(ctx, fp)
	if err != nil {
		return nil, err
	}
	out := make([]Match, 0, len(ents))
	for _, e := range ents {
		out = append(out, matchFromEntity(e, ConfidenceSymbolFingerprint,
			fmt.Sprintf("symbol_fingerprint == %q", fp)))
	}
	return out, nil
}

// ASTHash matches on `code.core.ast_hash` exact equality. Same data-
// availability caveat as SymbolFingerprint.
type ASTHash struct{}

// Kind returns "ast_hash".
func (ASTHash) Kind() string { return "ast_hash" }

// Evaluate returns every entity with the requested AST hash.
func (ASTHash) Evaluate(ctx context.Context, a *dsl.Anchor, store Lookup) ([]Match, error) {
	h := stringValue(a)
	if h == "" {
		return nil, nil
	}
	ents, err := store.LookupByASTHash(ctx, h)
	if err != nil {
		return nil, err
	}
	out := make([]Match, 0, len(ents))
	for _, e := range ents {
		out = append(out, matchFromEntity(e, ConfidenceASTHash,
			fmt.Sprintf("ast_hash == %q", h)))
	}
	return out, nil
}
