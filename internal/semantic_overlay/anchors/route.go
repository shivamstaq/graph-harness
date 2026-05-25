package anchors

import (
	"context"
	"fmt"
	"sort"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/shivamstaq/graph-harness/internal/dsl"
)

// Route-family anchor evaluators target code.framework Route entities.
// The code_framework EntityWriter materializes Route entities into the
// shared code.core store with a COMPOSITE qualified name so the
// change.process pipeline links a route to its handler + contract
// tests by a single key:
//
//	Entity.Kind          = "Route"
//	Entity.QualifiedName = "<METHOD> <path>" (e.g. "GET /v1/orders/:id")
//	Entity.KindTag       = the HTTP method (uppercase, e.g. "GET")
//
// route_pattern globs against the PATH PORTION of the qualified name
// (the substring after the first space) so a selector written
// `anchor route_pattern "/v1/orders/**"` binds the "GET /v1/orders/:id"
// row; route_method matches KindTag.

// RoutePattern matches Route entities whose path pattern satisfies a
// doublestar glob (SPEC §3.1 structural family). A glob over routes is
// stronger than a glob over file paths because the candidate set is
// narrower — every match is already known to be a Route — so the
// confidence sits between bound and reanchored.
type RoutePattern struct{}

// Kind returns "route_pattern".
func (RoutePattern) Kind() string { return "route_pattern" }

// ConfidenceRoutePattern is the score RoutePattern assigns to a glob
// match. Higher than path_glob because the kind filter has already
// narrowed the candidate set; lower than qualified_name because a
// glob can match multiple routes.
const ConfidenceRoutePattern = 0.80

// Evaluate filters Route entities by glob match against the path
// pattern. Empty value or invalid glob → no matches.
func (RoutePattern) Evaluate(ctx context.Context, a *dsl.Anchor, store Lookup) ([]Match, error) {
	pattern := stringValue(a)
	if pattern == "" {
		return nil, nil
	}
	if !doublestar.ValidatePattern(pattern) {
		return nil, fmt.Errorf("route_pattern anchor: invalid pattern %q", pattern)
	}
	ents, err := store.LookupAllEntities(ctx)
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, e := range ents {
		if string(e.Kind) != "Route" {
			continue
		}
		path := routePathPart(e.QualifiedName)
		ok, err := doublestar.PathMatch(pattern, path)
		if err != nil {
			return nil, fmt.Errorf("route_pattern: match %q vs %q: %w", pattern, path, err)
		}
		if ok {
			out = append(out, matchFromEntity(e, ConfidenceRoutePattern,
				fmt.Sprintf("route_pattern matches glob %q", pattern)))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].QualifiedName < out[j].QualifiedName
	})
	return out, nil
}

// routePathPart returns the path portion of a composite Route
// qualified name ("GET /v1/orders/:id" → "/v1/orders/:id"). A bare
// path with no method prefix is returned unchanged.
func routePathPart(qn string) string {
	for i := 0; i < len(qn); i++ {
		if qn[i] == ' ' {
			return qn[i+1:]
		}
	}
	return qn
}

// RouteMethod matches Route entities whose HTTP method equals the
// anchor's value (case-sensitive — the Pass 1 extractor canonicalizes
// methods to upper case). Eq match scores at full confidence; the
// resolver still needs a path-side anchor to pin to a specific route.
type RouteMethod struct{}

// Kind returns "route_method".
func (RouteMethod) Kind() string { return "route_method" }

// ConfidenceRouteMethod is the score RouteMethod assigns to an exact
// match. Method equality is a high-precision filter (only a handful
// of HTTP methods exist), but a method anchor alone matches many
// routes — confidence reflects "narrow but not unique."
const ConfidenceRouteMethod = 0.70

// Evaluate filters Route entities by exact match on KindTag (the
// canonical method storage slot for Route entities; see the package
// godoc for the convention).
func (RouteMethod) Evaluate(ctx context.Context, a *dsl.Anchor, store Lookup) ([]Match, error) {
	want := stringValue(a)
	if want == "" {
		return nil, nil
	}
	ents, err := store.LookupAllEntities(ctx)
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, e := range ents {
		if string(e.Kind) != "Route" {
			continue
		}
		if e.KindTag != want {
			continue
		}
		out = append(out, matchFromEntity(e, ConfidenceRouteMethod,
			fmt.Sprintf("route_method == %q", want)))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].QualifiedName < out[j].QualifiedName
	})
	return out, nil
}
