// Package express implements the routes.ts.express framework
// extractor (P2.T07). It detects HTTP routes declared via the
// Express.js router API: `app.get('/path', handler)`,
// `.post`/`.put`/`.patch`/`.delete`/`.head`/`.options`/`.all`,
// `app.use(subRouter)`, and `router.use('/api', subRouter)` for
// sub-router mounts.
//
// One Extractor instance lives per workspace. OnEvent reads the
// FileChanged payload, parses the TypeScript source with
// tree-sitter, and walks the AST collecting `<receiver>.<method>(...)`
// call expressions whose receiver is plausibly an Express
// app/router. The walker is intentionally lenient on receiver
// identity because Express has no required import shape — the
// fixture uses `app` and `router` but real code uses any binding.
//
// SPEC alignment:
//   - §2.2 selectors are the only cross-layer reference; Routes
//     point at handlers via SelectorRef with a qualified_name anchor
//     (preferred) and path_glob fallback.
//   - §6.21 compare-before-emit: identical re-parses produce
//     identical content ids and the Dispatcher suppresses the
//     emission.
package express

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

// extractorName is the registry key. Must match the package init()
// Register call so the Dispatcher can locate the constructor.
const extractorName = "routes.ts.express"

// frameworkLabel is the human-readable value emitted on every Route
// payload. Surfaced in CLI listings and used by the route_pattern
// anchor evaluator to filter Route candidates by framework.
const frameworkLabel = "express"

func init() {
	code_framework.Register(extractorName, newExtractor, code_framework.Descriptor{
		Name:       extractorName,
		Family:     "routes",
		Languages:  []string{"typescript"},
		Frameworks: []string{"express"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     []code_framework.EventKind{code_framework.InputCoreFileChanged},
		Outputs:    []code_framework.EntityKind{code_framework.KindRoute, code_framework.KindHandler},
	})
}

// extractor implements code_framework.Extractor.
type extractor struct {
	deps code_framework.Deps
}

func newExtractor(deps code_framework.Deps) (code_framework.Extractor, error) {
	return &extractor{deps: deps}, nil
}

// Name returns the registry key.
func (e *extractor) Name() string { return extractorName }

// Inputs returns the only event kind this extractor subscribes to:
// code.core.FileChanged. Sub-router mounts are detected by walking
// the parsed file; we do not need code.core.SymbolAdded events.
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
		Frameworks: []string{"express"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// OnEvent reads the FileChanged path, parses it as TypeScript, and
// emits one RouteAdded + one HandlerBound event per detected route.
// Non-TS paths and parse failures return (nil, nil) — the
// FileChanged event is observed but produces no framework events.
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
		// Best-effort: log via Deps.Logf but do not propagate;
		// upstream callers should not see "file vanished" errors
		// for routes specifically.
		if e.deps.Logf != nil {
			e.deps.Logf("routes.ts.express: parse %q: %v", p.Path, err)
		}
		return nil, nil
	}
	defer tree.Close()

	emissions := walkExpress(tree.RootNode(), src, p.Path)
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

// walkExpress recursively scans the AST for `<recv>.<method>(...)`
// call expressions whose method name is a recognized HTTP verb.
func walkExpress(root *tree_sitter.Node, src []byte, path string) []common.FrameworkEmission {
	var out []common.FrameworkEmission
	walk(root, src, path, &out)
	return out
}

// walk is the recursive AST visitor. It descends into every named
// child looking for call_expression nodes shaped like
// `<receiver>.<method>(...)`.
func walk(n *tree_sitter.Node, src []byte, path string, out *[]common.FrameworkEmission) {
	if n == nil {
		return
	}
	if n.Kind() == "call_expression" {
		if em, ok := matchExpressCall(n, src, path); ok {
			*out = append(*out, em)
			// Don't return: nested calls (e.g. middleware chains)
			// can themselves contain route calls in inline
			// callbacks.
		}
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		walk(n.NamedChild(i), src, path, out)
	}
}

// matchExpressCall inspects a call_expression and returns a
// FrameworkEmission if it shapes like an Express route registration.
func matchExpressCall(n *tree_sitter.Node, src []byte, path string) (common.FrameworkEmission, bool) {
	fn := n.ChildByFieldName("function")
	args := n.ChildByFieldName("arguments")
	if fn == nil || args == nil {
		return common.FrameworkEmission{}, false
	}
	if fn.Kind() != "member_expression" {
		return common.FrameworkEmission{}, false
	}
	property := fn.ChildByFieldName("property")
	if property == nil || property.Kind() != "property_identifier" {
		return common.FrameworkEmission{}, false
	}
	method := property.Utf8Text(src)
	if !common.IsHTTPMethodName(method) && !strings.EqualFold(method, "all") {
		return common.FrameworkEmission{}, false
	}

	// Arguments structure: `(path, ...middleware, handler)`.
	// We look at named children: the first must be a string /
	// template_string; the LAST is the handler (Express convention).
	pathLit, handlerNode, ok := extractPathAndHandler(args, src)
	if !ok {
		return common.FrameworkEmission{}, false
	}
	handler, ok := common.ExtractHandlerRef(handlerNode, src, path)
	if !ok {
		// Unknown handler shape — still emit at dynamic confidence
		// so the route is at least visible. Anchor the handler to
		// path_glob only.
		handler = common.HandlerRef{
			InlineSpan: fmt.Sprintf("%s:%d-%d", filepath.Base(path), handlerNode.StartByte(), handlerNode.EndByte()),
		}
	}
	method = common.CanonicalMethod(method)
	if strings.EqualFold(method, "ALL") {
		method = "ALL"
	}
	return common.FrameworkEmission{
		Method:      method,
		PathPattern: pathLit.Value,
		PathSource:  pathLit.Source,
		Handler:     handler,
	}, true
}

// extractPathAndHandler walks an `arguments` node to pull the path
// (first named child) and handler (last named child). Returns false
// if there are fewer than two named arguments.
func extractPathAndHandler(args *tree_sitter.Node, src []byte) (common.PathLiteral, *tree_sitter.Node, bool) {
	count := args.NamedChildCount()
	if count < 2 {
		return common.PathLiteral{}, nil, false
	}
	first := args.NamedChild(0)
	if first == nil {
		return common.PathLiteral{}, nil, false
	}
	last := args.NamedChild(count - 1)
	if last == nil {
		return common.PathLiteral{}, nil, false
	}
	pathLit, ok := common.ExtractStringLiteral(first, src)
	if !ok {
		// Dynamic path expression. Still recover a synthesized
		// pattern so the route is visible; downgrade source.
		pathLit = common.PathLiteral{
			Value:  first.Utf8Text(src),
			Source: "dynamic",
		}
	}
	return pathLit, last, true
}
