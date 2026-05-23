// Package common holds shared helpers for the TypeScript HTTP-route
// framework extractors (express, fastify). The exported surface is
// intentionally small: parsing entry points, route-shape detection
// helpers, and payload builders for code.framework Route/Handler
// entities. Per-framework packages compose these into their OnEvent
// pipeline.
//
// SPEC alignment:
//   - §2.2 selectors are the only legal cross-layer reference; Routes
//     anchor to their handler via SelectorRef with a qualified_name
//     anchor (preferred) and path_glob anchor (always present as a
//     fallback in case the qualified_name resolution misses).
//   - §6.11 tree-sitter parses are tolerant; partial files still
//     surface the routes that lie above the first parse error.
//   - §6.21 compare-before-emit: the dispatcher hashes our payload's
//     id; we MUST use code_framework.MakeContentID so identical route
//     definitions across rebuilds collapse onto the same row.
package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// FileChangedPayload mirrors the source.live/code.core convention
// `{"path": "<workspace-relative path>"}` for the FileChanged event.
// The TypeScript routes extractors react to this kind so they can
// re-parse the changed file.
type FileChangedPayload struct {
	Path string `json:"path"`
}

// IsTypeScriptPath reports whether the path is one of the file
// extensions the tree-sitter-typescript grammar covers
// (.ts/.tsx/.mts/.cts plus the JS family because the grammar
// double-duties as an "any-script" parser per parser_ts.go).
func IsTypeScriptPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs":
		return true
	}
	return false
}

// ParseFile reads `<workspace>/<rel>` and parses it as TypeScript /
// TSX, returning the root node + source bytes. Caller is responsible
// for closing the returned tree.
//
// The returned tree's lifetime is tied to the caller's stack frame —
// `defer tree.Close()` on the call site is mandatory.
func ParseFile(workspace, rel string) (*tree_sitter.Tree, []byte, error) {
	abs := rel
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(workspace, rel)
	}
	src, err := os.ReadFile(abs)
	if err != nil {
		return nil, nil, fmt.Errorf("read %q: %w", abs, err)
	}
	tree, err := ParseSource(rel, src)
	return tree, src, err
}

// ParseSource parses src as TypeScript / TSX (depending on the path
// extension) and returns the parsed tree. Mirrors
// source_live/parser_ts.go's grammar selection so the AST shapes we
// inspect line up with the rest of the codebase.
func ParseSource(path string, src []byte) (*tree_sitter.Tree, error) {
	parser := tree_sitter.NewParser()
	// Note: parser is NOT closed here — it's bound to the tree's
	// lifetime. tree-sitter-go documents Parser.Close as separate
	// from Tree.Close, but in practice freeing the parser before the
	// tree is freed is fine because Tree retains its own arena. We
	// close the parser at the end of this function and rely on the
	// tree's independent ownership of its arena.
	defer parser.Close()

	lang := tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript())
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tsx", ".jsx":
		lang = tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTSX())
	}
	if err := parser.SetLanguage(lang); err != nil {
		return nil, fmt.Errorf("set typescript language: %w", err)
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return nil, errors.New("tree-sitter Parse returned nil")
	}
	return tree, nil
}

// HTTPMethods is the canonical uppercase HTTP method set the
// extractors recognize. Express + Fastify both expose lower-case
// method calls (`app.get(...)`), so the matcher folds to upper case
// on emit. `all` and `route` are framework-specific and handled
// outside this list.
var HTTPMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

// IsHTTPMethodName reports whether the lower-case identifier names a
// canonical HTTP method (e.g. "get" → true, "register" → false).
func IsHTTPMethodName(s string) bool {
	for _, m := range HTTPMethods {
		if strings.EqualFold(s, m) {
			return true
		}
	}
	return false
}

// CanonicalMethod returns the upper-case form of a method
// identifier. Falls through unchanged for already-canonical input.
func CanonicalMethod(s string) string { return strings.ToUpper(s) }

// PathLiteral represents an extracted path string plus its
// confidence-modifying provenance. Express + Fastify both accept
// either a string literal or a template literal as the path arg.
type PathLiteral struct {
	// Value is the canonical path pattern. Template literal
	// interpolations are replaced by `${...}` placeholders so the
	// path remains comparable while staying honest about the dynamic
	// portion.
	Value string

	// Source classifies how the value was extracted, driving the
	// confidence rubric documented in the per-framework README:
	//   - "string"   — pure string literal → 0.95
	//   - "template" — template literal (no interpolations) → 0.95
	//   - "computed" — template literal with interpolations → 0.85
	//   - "dynamic"  — identifier / expression we could not unwrap → 0.60
	Source string
}

// Confidence translates Source into the rubric value.
func (p PathLiteral) Confidence() float64 {
	switch p.Source {
	case "string", "template":
		return 0.95
	case "computed":
		return 0.85
	case "dynamic":
		return 0.60
	}
	return 0.60
}

// ExtractStringLiteral pulls the canonical string value out of a
// `string` or `template_string` tree-sitter node. Returns false if
// the node is not one of the recognized shapes.
func ExtractStringLiteral(n *tree_sitter.Node, src []byte) (PathLiteral, bool) {
	if n == nil {
		return PathLiteral{}, false
	}
	switch n.Kind() {
	case "string":
		// Trim surrounding quote bytes. tree-sitter's `string` node
		// includes its delimiters as named children; the simplest
		// canonical text is the raw text minus the first/last byte.
		text := n.Utf8Text(src)
		if len(text) >= 2 {
			text = text[1 : len(text)-1]
		}
		return PathLiteral{Value: text, Source: "string"}, true
	case "template_string":
		// Walk children: `string_fragment` nodes contribute literal
		// text; `template_substitution` nodes become `${...}` so the
		// emitted pattern preserves shape without leaking the
		// interpolation source. If any substitution is present the
		// source drops to "computed".
		var b strings.Builder
		hasSub := false
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			switch c.Kind() {
			case "string_fragment":
				b.WriteString(c.Utf8Text(src))
			case "template_substitution":
				hasSub = true
				b.WriteString("${...}")
			}
		}
		src := "template"
		if hasSub {
			src = "computed"
		}
		return PathLiteral{Value: b.String(), Source: src}, true
	}
	return PathLiteral{Value: n.Utf8Text(src), Source: "dynamic"}, false
}

// HandlerRef classifies what the route's handler argument looks like:
// a named identifier, an arrow function, a function expression, or
// something else we cannot statically resolve. The emitted Route
// anchors its `AnchoredTo` SelectorRef on the qualified name when
// available; otherwise it falls back to a path_glob anchor.
type HandlerRef struct {
	// QualifiedName is empty when the handler is inline. When the
	// handler is `myHandler` the QualifiedName is the bare
	// identifier — the framework extractor knows nothing about the
	// module-level namespace, so the resolver applies the workspace
	// SCIP / LSP layers to disambiguate. Empty + InlineSpan != "" is
	// the inline-arrow case; we still emit a Handler entity anchored
	// on path_glob.
	QualifiedName string

	// InlineSpan is `path:start-end` for the inline arrow body when
	// QualifiedName is empty. Used as the Handler's path_glob anchor
	// value so two distinct inline routes do not collide.
	InlineSpan string
}

// ExtractHandlerRef pulls handler classification info from a
// tree-sitter node representing the handler argument. Returns
// `(HandlerRef{}, false)` for argument shapes we cannot classify
// (e.g. a call expression returning a closure).
func ExtractHandlerRef(n *tree_sitter.Node, src []byte, path string) (HandlerRef, bool) {
	if n == nil {
		return HandlerRef{}, false
	}
	switch n.Kind() {
	case "identifier":
		return HandlerRef{QualifiedName: n.Utf8Text(src)}, true
	case "member_expression":
		// `controllers.users.list` form. Use the textual rendering as
		// the qualified name; it is good enough for the SCIP / LSP
		// resolver layer.
		return HandlerRef{QualifiedName: n.Utf8Text(src)}, true
	case "arrow_function", "function_expression", "function":
		return HandlerRef{
			InlineSpan: fmt.Sprintf("%s:%d-%d", path, n.StartByte(), n.EndByte()),
		}, true
	}
	return HandlerRef{}, false
}

// FrameworkEmission is the unit a per-framework extractor produces.
// Returned by the shared walk so the package-level OnEvent can stamp
// it into a `kernel.Event` with a deterministic id and the right
// event Kind.
type FrameworkEmission struct {
	Method      string
	PathPattern string
	Framework   string
	PathSource  string
	Handler     HandlerRef
}

// BuildRouteEvent constructs a `kernel.Event{Kind: "RouteAdded"}`
// holding a code_framework.Route payload. The id uses MakeContentID
// over the canonical attribute set so identical route definitions
// emitted from re-extraction collapse via compare-before-emit.
//
// inputSeq is the upstream FileChanged event's Seq; it is recorded
// on Provenance.ProducedSeq so consumers can chain causality back
// to the originating file change without a separate event-graph
// walk.
func BuildRouteEvent(emission FrameworkEmission, path string, inputSeq uint64, extractorName string) (kernel.Event, kernel.Event, error) {
	anchor := buildHandlerAnchor(emission.Handler, path)

	attrs := map[string]any{
		"method":       emission.Method,
		"path_pattern": emission.PathPattern,
		"framework":    emission.Framework,
		"path":         path,
	}
	if emission.Handler.QualifiedName != "" {
		attrs["handler_qn"] = emission.Handler.QualifiedName
	}
	if emission.Handler.InlineSpan != "" {
		attrs["handler_span"] = emission.Handler.InlineSpan
	}
	routeID := code_framework.MakeContentID(code_framework.KindRoute, anchor, attrs)

	confidence := confidenceFor(emission.PathSource)
	prov := code_framework.Provenance{
		Confidence:  confidence,
		Freshness:   kernel.FreshnessCurrent,
		SourceClass: []kernel.SourceClass{code_framework.SourceExtractorFramework},
		ProducedBy:  string(code_framework.SourceExtractorFramework) + ":" + extractorName,
		ProducedSeq: inputSeq,
		Inputs:      []string{path},
	}
	route := code_framework.Route{
		ID:          routeID,
		Kind:        code_framework.KindRoute,
		Method:      emission.Method,
		PathPattern: emission.PathPattern,
		Framework:   emission.Framework,
		AnchoredTo:  anchor,
		Provenance:  prov,
	}
	routePayload, err := json.Marshal(route)
	if err != nil {
		return kernel.Event{}, kernel.Event{}, fmt.Errorf("marshal route: %w", err)
	}
	routeEvent := kernel.Event{
		Kind:    "RouteAdded",
		Payload: routePayload,
	}

	handler := code_framework.Handler{
		Kind:       code_framework.KindHandler,
		AnchoredTo: anchor,
		Provenance: prov,
	}
	handlerAttrs := map[string]any{
		"route_id": routeID,
	}
	if emission.Handler.QualifiedName != "" {
		handlerAttrs["qualified_name"] = emission.Handler.QualifiedName
	}
	if emission.Handler.InlineSpan != "" {
		handlerAttrs["inline_span"] = emission.Handler.InlineSpan
	}
	handler.ID = code_framework.MakeContentID(code_framework.KindHandler, anchor, handlerAttrs)
	handlerPayload, err := json.Marshal(handler)
	if err != nil {
		return kernel.Event{}, kernel.Event{}, fmt.Errorf("marshal handler: %w", err)
	}
	handlerEvent := kernel.Event{
		Kind:    "HandlerBound",
		Payload: handlerPayload,
	}
	return routeEvent, handlerEvent, nil
}

// buildHandlerAnchor prefers qualified_name when an identifier
// handler is available; otherwise it falls back to a path_glob
// anchor that scopes to the file containing the inline handler.
func buildHandlerAnchor(h HandlerRef, path string) code_framework.SelectorRef {
	anchors := make([]code_framework.Anchor, 0, 2)
	if h.QualifiedName != "" {
		anchors = append(anchors, code_framework.Anchor{
			Kind:  "qualified_name",
			Value: h.QualifiedName,
		})
	}
	// Always include a path_glob anchor; for inline handlers this is
	// the only identification we have, and for identifier handlers it
	// helps the resolver narrow the candidate set to the right module.
	anchors = append(anchors, code_framework.Anchor{
		Kind:  "path_glob",
		Value: path,
	})
	if h.InlineSpan != "" {
		anchors = append(anchors, code_framework.Anchor{
			Kind:  "body_hash",
			Value: h.InlineSpan,
		})
	}
	return code_framework.SelectorRef{
		Anchors: anchors,
		Unique:  h.QualifiedName != "" && h.InlineSpan == "",
	}
}

func confidenceFor(source string) float64 {
	switch source {
	case "string", "template":
		return 0.95
	case "computed":
		return 0.85
	case "dynamic":
		return 0.60
	}
	return 0.60
}
