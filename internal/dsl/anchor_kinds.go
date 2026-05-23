package dsl

import "fmt"

// AnchorKind is a closed enum of the anchor-kind strings the DSL accepts.
// Phase 2 Pass 0.5 (P2.T33): the existing seven kinds are joined by six
// framework-anchor kinds. The matching evaluators live in
// internal/semantic_overlay/anchors/ (Pass 0.5 Agent A); the grammar's job
// here is to reject typos and unknown kinds at parse time so authors get a
// helpful error pointing at the offending file:line.
//
// Keep this list in sync with the evaluator dispatch in semantic_overlay.
const (
	// Existing P0/P1 anchor kinds.
	AnchorKindQualifiedName     = "qualified_name"
	AnchorKindFunctionSignature = "function_signature"
	AnchorKindBodyHash          = "body_hash"
	AnchorKindCallNeighborhood  = "call_neighborhood"
	AnchorKindSymbolFingerprint = "symbol_fingerprint"
	AnchorKindASTHash           = "ast_hash"
	AnchorKindPathGlob          = "path_glob"
	AnchorKindLanguageID        = "language_id"

	// Phase 2 kindwise / framework anchor kinds.
	AnchorKindEntityKind   = "entity_kind"
	AnchorKindRoutePattern = "route_pattern"
	AnchorKindRouteMethod  = "route_method"
	AnchorKindEventName    = "event_name"
	AnchorKindTopicName    = "topic_name"
	AnchorKindSchemaField  = "schema_field"
	AnchorKindSchemaTable  = "schema_table"
)

// validAnchorKinds is the closed set of anchor identifiers ParseString
// accepts. Any other identifier produces an "unknown anchor kind" error
// at parse time. The vocabulary is intentionally closed: cross-agent
// pinning (Pass 0.5 A + B) requires both sides agree on the canonical
// string spelling.
var validAnchorKinds = map[string]struct{}{
	AnchorKindQualifiedName:     {},
	AnchorKindFunctionSignature: {},
	AnchorKindBodyHash:          {},
	AnchorKindCallNeighborhood:  {},
	AnchorKindSymbolFingerprint: {},
	AnchorKindASTHash:           {},
	AnchorKindPathGlob:          {},
	AnchorKindLanguageID:        {},
	AnchorKindEntityKind:        {},
	AnchorKindRoutePattern:      {},
	AnchorKindRouteMethod:       {},
	AnchorKindEventName:         {},
	AnchorKindTopicName:         {},
	AnchorKindSchemaField:       {},
	AnchorKindSchemaTable:       {},
}

// IsValidAnchorKind reports whether s is one of the canonical anchor-kind
// strings the resolver understands.
func IsValidAnchorKind(s string) bool {
	_, ok := validAnchorKinds[s]
	return ok
}

// validateAnchorKinds walks the file and rejects any anchor whose Kind is
// not in validAnchorKinds. Errors include the file:line position lifted
// from the Participle lexer so authors can jump to the offending site.
func validateAnchorKinds(f *File) error {
	for _, d := range f.Decls {
		if d.Selector != nil {
			for _, a := range d.Selector.Anchors {
				if !IsValidAnchorKind(a.Kind) {
					return fmt.Errorf("%s:%d:%d: unknown anchor kind %q (valid kinds: %s)",
						a.Pos.Filename, a.Pos.Line, a.Pos.Column, a.Kind, knownKindsList())
				}
			}
		}
		if d.Flow != nil {
			for _, st := range d.Flow.Steps {
				if st.Targets == nil || st.Targets.InlineSelector == nil {
					continue
				}
				for _, a := range st.Targets.InlineSelector.Anchors {
					if !IsValidAnchorKind(a.Kind) {
						return fmt.Errorf("%s:%d:%d: unknown anchor kind %q (valid kinds: %s)",
							a.Pos.Filename, a.Pos.Line, a.Pos.Column, a.Kind, knownKindsList())
					}
				}
			}
		}
	}
	return nil
}

// knownKindsList renders the closed set in a stable order for error
// messages. Order is the declaration order above (existing kinds first,
// then framework kinds) so users see the "core" vocabulary first.
func knownKindsList() string {
	return "qualified_name, function_signature, body_hash, call_neighborhood, " +
		"symbol_fingerprint, ast_hash, path_glob, language_id, entity_kind, " +
		"route_pattern, route_method, event_name, topic_name, schema_field, schema_table"
}
