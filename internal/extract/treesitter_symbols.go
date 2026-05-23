package extract

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/shivamstaq/graph-harness/internal/code_core/normalize"
	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// computeSymbolFingerprint produces a stable per-symbol fingerprint
// used by the symbol_fingerprint anchor evaluator (SPEC §3.3,
// P1.T25, F11). The fingerprint EXCLUDES the function/method body
// hash so rename-with-same-body still rebinds via this anchor; it
// includes language + qualified_name + receiver + normalized
// signature so the same logical symbol observed from different
// sources collapses to the same fingerprint.
//
// Mismatches signal "a different logical symbol with overlapping
// surface area" — the resolver's reanchored outcome.
func computeSymbolFingerprint(language, qname, receiver, signature string) string {
	ns := normalize.ForLanguage(language, signature)
	h := sha256.New()
	for _, s := range []string{language, qname, receiver, ns} {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// computeASTHash produces a per-symbol hash whose stability shape is
// the inverse of SymbolFingerprint: it INCLUDES the body hash (so
// edits inside the body shift it) and EXCLUDES qualified_name (so
// rename without body change does not). Combined with
// SymbolFingerprint the resolver gets two complementary rungs:
//
//   - SymbolFingerprint: stable across edits, breaks on signature change.
//   - ASTHash: stable across rename, breaks on body edit.
//
// Per F11: a real per-language AST-walk hash (e.g. tree-sitter child
// kind sequence) is a P2 polish; this v1 input set is the cheapest
// hash that satisfies the two-anchor complementary contract.
func computeASTHash(language, receiver, signature, bodyHash string) string {
	ns := normalize.ForLanguage(language, signature)
	h := sha256.New()
	for _, s := range []string{language, receiver, ns, bodyHash} {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ParsedFileToSymbols projects a tree-sitter ParsedFile into the
// uniform Symbol envelope so the unifier can fold tree-sitter facts
// alongside LSP and SCIP facts. Mirrors the per-language identity
// formula choices code_core.entityFromSymbol expects:
//
//   - functions emit SymbolKindFunction with QualifiedName + Signature.
//   - methods emit SymbolKindMethod with Receiver populated.
//
// SourceClass is always SourceClassTreesitter; ProducedBy carries the
// canonical extractor identifier so provenance.sources[] entries map
// 1:1 to what the watcher publishes. Confidence is 0.95 — high but not
// 1.0 because tree-sitter is structural, not type-aware (LSP/SCIP
// outrank it for the same canonical key).
func ParsedFileToSymbols(pf *source_live.ParsedFile) []source_live.Symbol {
	if pf == nil {
		return nil
	}
	out := make([]source_live.Symbol, 0, len(pf.Functions)+len(pf.TypeDecls))
	for _, fn := range pf.Functions {
		kind := source_live.SymbolKindFunction
		if fn.Receiver != "" {
			kind = source_live.SymbolKindMethod
		}
		out = append(out, source_live.Symbol{
			Name:          fn.Name,
			QualifiedName: fn.QualifiedName,
			Kind:          kind,
			Range: source_live.Range{
				StartByte: fn.StartByte,
				EndByte:   fn.EndByte,
				StartLine: fn.StartLine,
				EndLine:   fn.EndLine,
			},
			Signature:         fn.Signature,
			LanguageID:        pf.Language,
			Path:              pf.Path,
			Receiver:          fn.Receiver,
			BodyHash:          fn.BodyHash,
			SymbolFingerprint: computeSymbolFingerprint(pf.Language, fn.QualifiedName, fn.Receiver, fn.Signature),
			ASTHash:           computeASTHash(pf.Language, fn.Receiver, fn.Signature, fn.BodyHash),
			SourceClass:       source_live.SourceClassTreesitter,
			ProducedBy:        "extractor:treesitter",
			Confidence:        0.95,
		})
	}
	for _, td := range pf.TypeDecls {
		kind := source_live.SymbolKindClass
		if td.Kind == source_live.TypeDeclKindInterface {
			kind = source_live.SymbolKindInterface
		}
		out = append(out, source_live.Symbol{
			Name:          td.Name,
			QualifiedName: td.QualifiedName,
			Kind:          kind,
			Range: source_live.Range{
				StartByte: td.StartByte,
				EndByte:   td.EndByte,
				StartLine: td.StartLine,
				EndLine:   td.EndLine,
			},
			LanguageID: pf.Language,
			Path:       pf.Path,
			BodyHash:   td.BodyHash,
			// Classes/interfaces have no signature; fingerprints derive
			// from the same identity inputs the resolver expects so the
			// symbol_fingerprint / ast_hash anchors still bind.
			SymbolFingerprint: computeSymbolFingerprint(pf.Language, td.QualifiedName, "", ""),
			ASTHash:           computeASTHash(pf.Language, "", "", td.BodyHash),
			SourceClass:       source_live.SourceClassTreesitter,
			ProducedBy:        "extractor:treesitter",
			Confidence:        0.95,
		})
	}
	return out
}
