// Package fastify implements the routes.ts.fastify framework
// extractor (P2.T07). It detects HTTP routes declared via the
// Fastify API:
//
//   - `fastify.get('/path', handler)` and the rest of the
//     per-method shortcuts (post/put/patch/delete/head/options).
//   - `fastify.route({ method: 'GET', url: '/path', handler })`
//     where method may be a single string or an array.
//   - `fastify.register(plugin, { prefix: '/api' })` — emitted as
//     an informational route-mount-style entity at low confidence
//     because the routes themselves live inside the plugin
//     function (the plugin's own file/parse produces them).
//
// Implementation mirrors the express extractor structurally so the
// two stay in shape. Differences are isolated to matchFastifyCall.
package fastify

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/shivamstaq/graph-harness/extractors/route/ts/common"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

const extractorName = "routes.ts.fastify"
const frameworkLabel = "fastify"

func init() {
	code_framework.Register(extractorName, newExtractor, code_framework.Descriptor{
		Name:       extractorName,
		Family:     "routes",
		Languages:  []string{"typescript"},
		Frameworks: []string{"fastify"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     []code_framework.EventKind{code_framework.InputCoreFileChanged},
		Outputs:    []code_framework.EntityKind{code_framework.KindRoute, code_framework.KindHandler},
	})
}

type extractor struct {
	deps code_framework.Deps
}

func newExtractor(deps code_framework.Deps) (code_framework.Extractor, error) {
	return &extractor{deps: deps}, nil
}

// Name returns the registry key.
func (e *extractor) Name() string { return extractorName }

// Inputs returns the only event kind this extractor subscribes to.
func (e *extractor) Inputs() []code_framework.EventKind {
	return []code_framework.EventKind{code_framework.InputCoreFileChanged}
}

// Outputs declares the entity kinds this extractor produces.
func (e *extractor) Outputs() []code_framework.EntityKind {
	return []code_framework.EntityKind{code_framework.KindRoute, code_framework.KindHandler}
}

// Capabilities advertises the extractor's static profile.
func (e *extractor) Capabilities() code_framework.Capabilities {
	return code_framework.Capabilities{
		Family:     "routes",
		Languages:  []string{"typescript"},
		Frameworks: []string{"fastify"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// OnEvent processes one FileChanged event. Returns nil when the
// path is non-TS or the file fails to parse — defensive because
// upstream code.core may report transient changes (e.g. mid-write
// snapshots) that we should not surface as extractor failures.
func (e *extractor) OnEvent(_ context.Context, in kernel.Event) ([]kernel.Event, error) {
	var p common.FileChangedPayload
	if err := json.Unmarshal(in.Payload, &p); err != nil {
		return nil, fmt.Errorf("unmarshal FileChanged payload: %w", err)
	}
	if p.Path == "" {
		return nil, nil
	}
	if !common.IsTypeScriptPath(p.Path) {
		return nil, nil
	}

	tree, src, err := common.ParseFile(e.deps.Workspace, p.Path)
	if err != nil {
		if e.deps.Logf != nil {
			e.deps.Logf("routes.ts.fastify: parse %q: %v", p.Path, err)
		}
		return nil, nil
	}
	defer tree.Close()

	emissions := walkFastify(tree.RootNode(), src, p.Path)
	out := make([]kernel.Event, 0, len(emissions)*2)
	for _, em := range emissions {
		em.Framework = frameworkLabel
		route, handler, err := common.BuildRouteEvent(em, p.Path, in.Seq, extractorName)
		if err != nil {
			return nil, err
		}
		out = append(out, route, handler)
	}
	return out, nil
}

func walkFastify(root *tree_sitter.Node, src []byte, path string) []common.FrameworkEmission {
	var out []common.FrameworkEmission
	walk(root, src, path, &out)
	return out
}

func walk(n *tree_sitter.Node, src []byte, path string, out *[]common.FrameworkEmission) {
	if n == nil {
		return
	}
	if n.Kind() == "call_expression" {
		for _, em := range matchFastifyCall(n, src, path) {
			*out = append(*out, em)
		}
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		walk(n.NamedChild(i), src, path, out)
	}
}

// matchFastifyCall returns zero or more emissions for a single
// call_expression. The .route form may declare multiple methods via
// an array, in which case we emit one Route per method.
func matchFastifyCall(n *tree_sitter.Node, src []byte, path string) []common.FrameworkEmission {
	fn := n.ChildByFieldName("function")
	args := n.ChildByFieldName("arguments")
	if fn == nil || args == nil {
		return nil
	}
	if fn.Kind() != "member_expression" {
		return nil
	}
	property := fn.ChildByFieldName("property")
	if property == nil || property.Kind() != "property_identifier" {
		return nil
	}
	method := property.Utf8Text(src)

	switch {
	case common.IsHTTPMethodName(method):
		return matchPerMethodCall(method, args, src, path)
	case strings.EqualFold(method, "route"):
		return matchRouteObjectCall(args, src, path)
	case strings.EqualFold(method, "register"):
		return matchRegisterCall(args, src, path)
	}
	return nil
}

// matchPerMethodCall handles `fastify.get('/path', opts?, handler)`.
// Mirrors the express shape but uses Fastify's "last arg is handler
// OR opts object containing handler" convention. We treat the last
// argument as the handler; if it is an object literal the resolver
// can later refine.
func matchPerMethodCall(method string, args *tree_sitter.Node, src []byte, path string) []common.FrameworkEmission {
	count := args.NamedChildCount()
	if count < 2 {
		return nil
	}
	first := args.NamedChild(0)
	last := args.NamedChild(count - 1)
	if first == nil || last == nil {
		return nil
	}
	pathLit, ok := common.ExtractStringLiteral(first, src)
	if !ok {
		pathLit = common.PathLiteral{Value: first.Utf8Text(src), Source: "dynamic"}
	}
	handler, ok := common.ExtractHandlerRef(last, src, path)
	if !ok {
		// Last arg might be an object literal `{ schema, handler }` —
		// dig out the `handler` property as the handler reference.
		if h, hok := handlerFromObject(last, src); hok {
			handler = h
		} else {
			handler = common.HandlerRef{
				InlineSpan: fmt.Sprintf("%s:%d-%d", filepath.Base(path), last.StartByte(), last.EndByte()),
			}
		}
	}
	return []common.FrameworkEmission{{
		Method:      common.CanonicalMethod(method),
		PathPattern: pathLit.Value,
		PathSource:  pathLit.Source,
		Handler:     handler,
	}}
}

// matchRouteObjectCall handles `fastify.route({ method, url, handler })`.
// `method` may be a string OR an array of strings. We emit one Route
// per method/url pair.
func matchRouteObjectCall(args *tree_sitter.Node, src []byte, path string) []common.FrameworkEmission {
	if args.NamedChildCount() == 0 {
		return nil
	}
	first := args.NamedChild(0)
	if first == nil || first.Kind() != "object" {
		return nil
	}
	methods, url, handlerNode := parseRouteObject(first, src)
	if len(methods) == 0 || url.Value == "" {
		return nil
	}
	var handler common.HandlerRef
	if handlerNode != nil {
		if h, ok := common.ExtractHandlerRef(handlerNode, src, path); ok {
			handler = h
		}
	}
	out := make([]common.FrameworkEmission, 0, len(methods))
	for _, m := range methods {
		out = append(out, common.FrameworkEmission{
			Method:      common.CanonicalMethod(m),
			PathPattern: url.Value,
			PathSource:  url.Source,
			Handler:     handler,
		})
	}
	return out
}

// matchRegisterCall covers `fastify.register(plugin, { prefix: '/api' })`.
// The plugin's own routes are extracted from the plugin's source file
// when that file changes; the register call itself does not emit
// Route entities. We currently surface nothing here so the matrix
// stays honest. Returns nil.
func matchRegisterCall(_ *tree_sitter.Node, _ []byte, _ string) []common.FrameworkEmission {
	// Intentionally empty. Plugins are observed via their own
	// FileChanged events. Returning nil keeps the test fixtures and
	// the route inventory in 1:1 correspondence with literal route
	// declarations.
	return nil
}

// parseRouteObject pulls `method`, `url`, and `handler` out of an
// object literal. Returns the methods as a slice (single-element for
// the string case).
func parseRouteObject(obj *tree_sitter.Node, src []byte) ([]string, common.PathLiteral, *tree_sitter.Node) {
	var methods []string
	var url common.PathLiteral
	var handlerNode *tree_sitter.Node
	for i := uint(0); i < obj.NamedChildCount(); i++ {
		c := obj.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Kind() != "pair" {
			continue
		}
		key := c.ChildByFieldName("key")
		value := c.ChildByFieldName("value")
		if key == nil || value == nil {
			continue
		}
		keyName := canonicalKey(key, src)
		switch keyName {
		case "method":
			methods = extractMethods(value, src)
		case "url", "path":
			if lit, ok := common.ExtractStringLiteral(value, src); ok {
				url = lit
			} else {
				url = common.PathLiteral{Value: value.Utf8Text(src), Source: "dynamic"}
			}
		case "handler":
			handlerNode = value
		}
	}
	return methods, url, handlerNode
}

// extractMethods unwraps either a string literal or an array of
// string literals into a slice of method names.
func extractMethods(n *tree_sitter.Node, src []byte) []string {
	switch n.Kind() {
	case "string":
		if lit, ok := common.ExtractStringLiteral(n, src); ok {
			return []string{lit.Value}
		}
	case "array":
		var out []string
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			if lit, ok := common.ExtractStringLiteral(c, src); ok {
				out = append(out, lit.Value)
			}
		}
		return out
	}
	return nil
}

// canonicalKey returns the textual key name from a `property_identifier`
// or quoted `string` key.
func canonicalKey(n *tree_sitter.Node, src []byte) string {
	switch n.Kind() {
	case "property_identifier":
		return n.Utf8Text(src)
	case "string":
		text := n.Utf8Text(src)
		if len(text) >= 2 {
			return text[1 : len(text)-1]
		}
		return text
	}
	return n.Utf8Text(src)
}

// handlerFromObject looks for a `handler` property in an object
// literal (the options-bag form of Fastify's per-method shortcuts).
func handlerFromObject(n *tree_sitter.Node, src []byte) (common.HandlerRef, bool) {
	if n.Kind() != "object" {
		return common.HandlerRef{}, false
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil || c.Kind() != "pair" {
			continue
		}
		key := c.ChildByFieldName("key")
		value := c.ChildByFieldName("value")
		if key == nil || value == nil {
			continue
		}
		if canonicalKey(key, src) == "handler" {
			return common.ExtractHandlerRef(value, src, "")
		}
	}
	return common.HandlerRef{}, false
}
