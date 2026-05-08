package anchors

import (
	"context"
	"fmt"
	"sort"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/dsl"
)

// LanguageID matches every entity whose `language_id` equals the anchor's
// value. Used to scope a polyglot selector to one language even when the
// same canonical_name exists across all three (e.g. CheckoutValidator.validate
// in Go + TypeScript + Python).
//
// Accepted values mirror the per-language normalizer dispatch table:
//
//	go (also: golang)
//	ts (also: typescript, javascript, js, tsx, jsx)
//	py (also: python)
//
// Confidence is intentionally low because language_id alone is a weak
// discriminator — many entities share a language. Use it as a *filter*
// alongside higher-precision anchors, not as a primary.
type LanguageID struct{}

// Kind returns "language_id".
func (LanguageID) Kind() string { return "language_id" }

// Evaluate scans every entity and reports those whose LanguageID matches
// the requested value (case-insensitive, with the same normalization
// applied as the signature dispatch table).
func (LanguageID) Evaluate(ctx context.Context, a *dsl.Anchor, store Lookup) ([]Match, error) {
	want := normalizeLanguageAlias(stringValue(a))
	if want == "" {
		return nil, nil
	}
	ents, err := store.LookupAllEntities(ctx)
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, e := range ents {
		if normalizeLanguageAlias(e.LanguageID) != want {
			continue
		}
		out = append(out, matchFromEntity(e, ConfidenceLanguageID,
			fmt.Sprintf("language_id == %q", want)))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].QualifiedName < out[j].QualifiedName
	})
	return out, nil
}

// normalizeLanguageAlias maps the SPEC §11.2 language identifiers + their
// common aliases onto the three canonical IDs the rest of the code base
// uses: "go", "ts", "py". Unknown identifiers pass through verbatim so a
// selector targeting some future language still matches entities tagged
// with that exact LanguageID.
func normalizeLanguageAlias(id string) string {
	low := make([]byte, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		low[i] = c
	}
	switch string(low) {
	case "go", "golang":
		return "go"
	case "ts", "typescript", "javascript", "js", "tsx", "jsx":
		return "ts"
	case "py", "python":
		return "py"
	}
	return string(low)
}

// Compile-time check that the package keeps a code_core godoc cross-link.
var _ = code_core.Entity{}
