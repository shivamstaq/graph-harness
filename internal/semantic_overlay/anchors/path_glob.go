package anchors

import (
	"context"
	"fmt"
	"sort"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/shivamstaq/graph-harness/internal/dsl"
)

// PathGlob matches entities whose `path` attribute satisfies a doublestar
// glob (SPEC §3.1 structural family). Glob alone is a weak signal — many
// entities share a path — so the score is intentionally low; a path_glob
// anchor below the reanchored threshold is the canonical "fallback" rung.
type PathGlob struct{}

// Kind returns "path_glob".
func (PathGlob) Kind() string { return "path_glob" }

// Evaluate scans every entity and reports those whose path matches the
// glob pattern.
func (PathGlob) Evaluate(ctx context.Context, a *dsl.Anchor, store Lookup) ([]Match, error) {
	pattern := stringValue(a)
	if pattern == "" {
		return nil, nil
	}
	if !doublestar.ValidatePattern(pattern) {
		return nil, fmt.Errorf("path_glob anchor: invalid pattern %q", pattern)
	}
	ents, err := store.LookupAllEntities(ctx)
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, e := range ents {
		if e.Path == "" {
			continue
		}
		ok, err := doublestar.PathMatch(pattern, e.Path)
		if err != nil {
			return nil, fmt.Errorf("path_glob anchor: match %q vs %q: %w", pattern, e.Path, err)
		}
		if ok {
			out = append(out, matchFromEntity(e, ConfidencePathGlob,
				fmt.Sprintf("path matches glob %q", pattern)))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].QualifiedName < out[j].QualifiedName
	})
	return out, nil
}
