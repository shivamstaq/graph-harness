// Package fastapi implements the FastAPI HTTP-route extractor
// (P2.T08 / Pass 1 1-Routes-Py). It detects decorator patterns of the
// form `@app.get("/path")`, `@router.post("/path")`, etc., and emits
// one Route + Handler per match.
//
// Limitations (documented in extractors/route/py/README.md):
//   - APIRouter prefix is detected at the module level (literal kwarg
//     `prefix="..."`); per-request mounted routers across modules
//     resolve at confidence 0.85.
//   - Class-based views (`@cbv` from fastapi-utils) are out of scope
//     for v1.
//   - Conditional `app.include_router(other_router)` is not chained;
//     each router emits its own routes anchored to its own prefix.
package fastapi

import (
	"context"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/shivamstaq/graph-harness/extractors/route/py/common"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Name is the registry name for this extractor.
const Name = "routes.py.fastapi"

// fastApiMethods is the set of HTTP method attributes FastAPI's
// `APIRouter` / `FastAPI` instances expose as decorators.
var fastApiMethods = map[string]string{
	"get":     "GET",
	"post":    "POST",
	"put":     "PUT",
	"delete":  "DELETE",
	"patch":   "PATCH",
	"head":    "HEAD",
	"options": "OPTIONS",
	"trace":   "TRACE",
}

// extractor is the routes.py.fastapi implementation.
type extractor struct {
	deps code_framework.Deps
}

// New builds a new FastAPI route extractor. Exposed for the
// registry's Constructor type; the daemon owns the lifecycle.
func New(deps code_framework.Deps) (code_framework.Extractor, error) {
	return &extractor{deps: deps}, nil
}

func (e *extractor) Name() string { return Name }

func (e *extractor) Inputs() []code_framework.EventKind {
	return []code_framework.EventKind{code_framework.InputCoreFileChanged}
}

func (e *extractor) Outputs() []code_framework.EntityKind {
	return []code_framework.EntityKind{code_framework.KindRoute, code_framework.KindHandler}
}

func (e *extractor) Capabilities() code_framework.Capabilities {
	return code_framework.Capabilities{
		Family:     "routes",
		Languages:  []string{"python"},
		Frameworks: []string{"fastapi"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// OnEvent is invoked per FileChanged event. Non-Python paths return
// nil (the dispatcher routes them via Inputs but the per-extractor
// path filter is the extractor's responsibility).
func (e *extractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	payload, err := common.DecodeFileChanged(in)
	if err != nil {
		return nil, err
	}
	if !common.IsPythonPath(payload.Path) {
		return nil, nil
	}
	src, err := common.ReadSource(e.deps.Workspace, payload.Path)
	if err != nil {
		return nil, err
	}
	if len(src) == 0 {
		return nil, nil
	}
	parsed, err := common.ParsePython(src)
	if err != nil {
		return nil, err
	}
	defer parsed.Close()

	routes := e.detect(parsed.Root(), parsed.Src, payload.Path)
	if len(routes) == 0 {
		return nil, nil
	}
	return common.ToEvents(Name, routes)
}

// detect walks the parsed tree, collecting routes from decorators
// such as `@app.get("/path")` or `@router.post("/path")`.
func (e *extractor) detect(root *tree_sitter.Node, src []byte, path string) []common.ExtractedRoute {
	module := common.ModuleName(path)
	// Pass 1: collect router/app instances and their `prefix=` kwargs
	// so decorator targets resolve to the right base path. This walks
	// only assignment statements at module scope.
	prefixes := collectRouterPrefixes(root, src)

	var routes []common.ExtractedRoute
	common.WalkDecoratedFuncs(root, src, func(classCtx string, df common.DecoratedFunc) {
		for _, dec := range df.Decorators {
			r, ok := e.routeFromDecorator(dec, src, module, classCtx, df.FuncName, prefixes, path)
			if ok {
				routes = append(routes, r)
			}
		}
	})
	return routes
}

// collectRouterPrefixes walks module-level assignments looking for
// `router = APIRouter(prefix="/api/v1")` and records router → prefix.
// FastAPI's idiom is to mount routers via app.include_router(router,
// prefix="..."); the include-side prefix is not parsed here in v1
// because cross-module resolution is out of scope.
func collectRouterPrefixes(root *tree_sitter.Node, src []byte) map[string]string {
	out := map[string]string{}
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Kind() == "expression_statement" {
			c = c.NamedChild(0)
			if c == nil {
				continue
			}
		}
		if c.Kind() != "assignment" {
			continue
		}
		left := c.ChildByFieldName("left")
		right := c.ChildByFieldName("right")
		if left == nil || right == nil {
			continue
		}
		varName := common.NodeText(left, src)
		// Look for `APIRouter(...)` or `FastAPI(...)` calls.
		if right.Kind() != "call" {
			continue
		}
		fn := right.ChildByFieldName("function")
		fnName := common.NodeText(fn, src)
		// Accept both unqualified and module-qualified names.
		shortName := fnName
		if idx := strings.LastIndex(shortName, "."); idx >= 0 {
			shortName = shortName[idx+1:]
		}
		if shortName != "APIRouter" && shortName != "FastAPI" {
			continue
		}
		args := right.ChildByFieldName("arguments")
		prefixNode := common.KeywordArg(args, "prefix", src)
		if prefixNode == nil {
			out[varName] = ""
			continue
		}
		if prefix, ok := common.StringLiteralValue(prefixNode, src); ok {
			out[varName] = prefix
		} else {
			// Computed prefix — track receiver but flag downstream.
			out[varName] = ""
		}
	}
	return out
}

// routeFromDecorator maps a `decorator` node to an ExtractedRoute.
// Returns (_, false) when the decorator is not a FastAPI route decorator.
func (e *extractor) routeFromDecorator(
	dec *tree_sitter.Node,
	src []byte,
	module, classCtx, fnName string,
	prefixes map[string]string,
	path string,
) (common.ExtractedRoute, bool) {
	dc := common.ParseDecorator(dec, src)
	if dc.Attr == "" {
		return common.ExtractedRoute{}, false
	}
	method, ok := fastApiMethods[dc.Attr]
	if !ok {
		return common.ExtractedRoute{}, false
	}
	// First positional arg is the path string.
	pathPattern, literal := common.PositionalString(dc.Args, src)
	confidence := common.ConfidenceLiteral
	if !literal {
		// Path is computed (f-string, variable, etc.). Emit a
		// placeholder pattern so the Route still surfaces; the lower
		// confidence flags the unreliable substring.
		pathPattern = "<computed>"
		confidence = common.ConfidenceComputed
	}

	// Apply receiver prefix if known.
	prefix := prefixes[dc.Receiver]
	full := common.JoinPath(prefix, pathPattern)

	// response_model= kwarg surfaces as ResponseKind when literal-ish.
	respKind := ""
	if rm := common.KeywordArg(dc.Args, "response_model", src); rm != nil {
		respKind = common.NodeText(rm, src)
	} else if rc := common.KeywordArg(dc.Args, "response_class", src); rc != nil {
		respKind = common.NodeText(rc, src)
	}

	qn := common.QualifiedName(common.QualifiedName(module, classCtx), fnName)
	return common.ExtractedRoute{
		Method:       method,
		PathPattern:  full,
		Framework:    "fastapi",
		ResponseKind: respKind,
		HandlerQN:    qn,
		Confidence:   confidence,
		SourcePath:   path,
	}, true
}

// compile-time assertion.
var _ code_framework.Extractor = (*extractor)(nil)

func init() {
	code_framework.Register(Name, New, code_framework.Descriptor{
		Family:     "routes",
		Languages:  []string{"python"},
		Frameworks: []string{"fastapi"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     []code_framework.EventKind{code_framework.InputCoreFileChanged},
		Outputs:    []code_framework.EntityKind{code_framework.KindRoute, code_framework.KindHandler},
	})
}
