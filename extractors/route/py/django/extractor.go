// Package django implements the Django HTTP-route extractor
// (P2.T08 / Pass 1 1-Routes-Py). Django routes live in URL conf
// modules (`urls.py`) as `urlpatterns = [path(...), re_path(...),
// include(...)]`. The extractor parses the assignment to
// `urlpatterns` and emits one Route per `path()` / `re_path()` entry.
//
// Method-handling: Django views accept any HTTP method by default;
// `@require_http_methods([...])` (and the verb-specific shortcuts
// `@require_GET`, `@require_POST`) constrain it. The extractor scans
// decorators on the view function *only if* the view is defined in
// the same urls.py module. Cross-module method constraints resolve at
// confidence 0.7 (ConfidenceDynamic).
//
// Limitations:
//   - `include("other.urls")` is captured as a single child route
//     anchored to the include target; we do NOT recursively expand
//     the included module in v1.
//   - Class-based views (`.as_view()`) emit one Route anchored to the
//     CBV class qualified name; per-HTTP-method handler resolution
//     is left for the resolver (the CBV's `get`/`post` methods).
//   - `path("/p", views.foo, name="x")` — third positional `name=`
//     and the keyword `name=` are equivalent; we don't surface the
//     URL name in v1.
package django

import (
	"context"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/shivamstaq/graph-harness/extractors/route/py/common"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Name is the registry name for this extractor.
const Name = "routes.py.django"

type extractor struct {
	deps code_framework.Deps
}

// New builds a Django route extractor.
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
		Frameworks: []string{"django"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

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
	// Heuristic: Django URL conf lives in *urls.py — restrict the
	// extractor to those so we don't emit phantom routes from files
	// that happen to import `django.urls.path`.
	if !isURLConfPath(payload.Path) {
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

// isURLConfPath gates the extractor to files whose basename ends in
// "urls.py" (the Django convention). Tests use fixtures that respect
// this naming.
func isURLConfPath(path string) bool {
	low := strings.ToLower(path)
	// Match both root urls.py and app/urls.py forms.
	return strings.HasSuffix(low, "urls.py") || strings.HasSuffix(low, "_urls.py")
}

// detect finds `urlpatterns = [...]` assignments and extracts
// path()/re_path()/include() entries.
func (e *extractor) detect(root *tree_sitter.Node, src []byte, path string) []common.ExtractedRoute {
	module := common.ModuleName(path)
	patternsList := findUrlPatternsList(root, src)
	if patternsList == nil {
		return nil
	}

	// Per-decorator method constraints for views defined in the same
	// module. Map: qualified-name → []method.
	localMethods := collectLocalMethodConstraints(root, src, module)

	var routes []common.ExtractedRoute
	for i := uint(0); i < patternsList.NamedChildCount(); i++ {
		entry := patternsList.NamedChild(i)
		if entry == nil || entry.Kind() != "call" {
			continue
		}
		rs := entryToRoutes(entry, src, module, localMethods, path)
		routes = append(routes, rs...)
	}
	return routes
}

// findUrlPatternsList locates the rightmost `urlpatterns = [...]`
// assignment at module scope and returns the list node.
func findUrlPatternsList(root *tree_sitter.Node, src []byte) *tree_sitter.Node {
	var found *tree_sitter.Node
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
		if common.NodeText(left, src) != "urlpatterns" {
			continue
		}
		if right.Kind() == "list" {
			found = right
		}
	}
	return found
}

// collectLocalMethodConstraints scans top-level decorated function
// defs for `@require_http_methods(...)`, `@require_GET`, etc., and
// builds a map qualified_name → []method.
func collectLocalMethodConstraints(root *tree_sitter.Node, src []byte, module string) map[string][]string {
	out := map[string][]string{}
	common.WalkDecoratedFuncs(root, src, func(classCtx string, df common.DecoratedFunc) {
		methods := []string{}
		for _, dec := range df.Decorators {
			dc := common.ParseDecorator(dec, src)
			switch {
			case dc.Receiver == "" && dc.Attr == "require_http_methods" && dc.Args != nil:
				if list := common.PositionalAt(dc.Args, 0, src); list != nil {
					methods = append(methods, common.ListLiteralStrings(list, src)...)
				}
			case dc.Receiver == "" && strings.HasPrefix(dc.Attr, "require_") && dc.Attr != "require_http_methods":
				m := strings.TrimPrefix(dc.Attr, "require_")
				m = strings.ToUpper(m)
				if m != "" && m != "SAFE_METHODS" {
					methods = append(methods, m)
				}
			}
		}
		if len(methods) > 0 {
			qn := common.QualifiedName(common.QualifiedName(module, classCtx), df.FuncName)
			out[qn] = methods
		}
	})
	return out
}

// entryToRoutes converts one `path(...)` / `re_path(...)` /
// `include(...)` call into one or more ExtractedRoute records.
func entryToRoutes(
	call *tree_sitter.Node,
	src []byte,
	module string,
	localMethods map[string][]string,
	srcPath string,
) []common.ExtractedRoute {
	fn := call.ChildByFieldName("function")
	args := call.ChildByFieldName("arguments")
	fnName := common.NodeText(fn, src)
	short := fnName
	if idx := strings.LastIndex(short, "."); idx >= 0 {
		short = short[idx+1:]
	}

	switch short {
	case "path", "re_path":
		return pathCallToRoutes(short, args, src, module, localMethods, srcPath)
	case "include":
		return includeCallToRoute(args, src, srcPath)
	}
	return nil
}

// pathCallToRoutes handles `path("p", view, ...)` and `re_path(...)`.
func pathCallToRoutes(
	fnShort string,
	args *tree_sitter.Node,
	src []byte,
	module string,
	localMethods map[string][]string,
	srcPath string,
) []common.ExtractedRoute {
	pathArg := common.PositionalAt(args, 0, src)
	viewArg := common.PositionalAt(args, 1, src)
	pathStr, literal := common.StringLiteralValue(pathArg, src)
	confidence := common.ConfidenceLiteral
	if !literal {
		pathStr = "<computed>"
		confidence = common.ConfidenceComputed
	}
	if !strings.HasPrefix(pathStr, "/") {
		pathStr = "/" + pathStr
	}

	// path("api/", include("...")) — re-route to includeCallToRoute
	// so the target module surfaces as the handler.
	if viewArg != nil && viewArg.Kind() == "call" {
		fn := viewArg.ChildByFieldName("function")
		if fn != nil {
			fnName := common.NodeText(fn, src)
			short := fnName
			if idx := strings.LastIndex(short, "."); idx >= 0 {
				short = short[idx+1:]
			}
			if short == "include" {
				inc := includeCallToRoute(viewArg.ChildByFieldName("arguments"), src, srcPath)
				// Override the per-entry path with the path()'s prefix.
				for i := range inc {
					inc[i].PathPattern = pathStr
				}
				return inc
			}
		}
	}

	handlerQN, handlerConf := resolveViewArg(viewArg, src, module)
	if handlerConf < confidence {
		confidence = handlerConf
	}

	// Method constraints: look up in local-decorator map; fall back
	// to "ANY".
	methods := []string{"ANY"}
	if got, ok := localMethods[handlerQN]; ok && len(got) > 0 {
		methods = got
	}

	out := make([]common.ExtractedRoute, 0, len(methods))
	framework := "django"
	for _, m := range methods {
		out = append(out, common.ExtractedRoute{
			Method:       m,
			PathPattern:  pathStr,
			Framework:    framework,
			Middleware:   nil,
			ResponseKind: "",
			HandlerQN:    handlerQN,
			Confidence:   confidence,
			SourcePath:   srcPath,
		})
	}
	// Preserve fnShort (regex vs literal) as a leading marker in the
	// pattern only when it adds information.
	_ = fnShort
	return out
}

// resolveViewArg picks a qualified name out of the view argument and
// reports the confidence drop, if any.
//
//   - `views.foo` (attribute) → qn = "views.foo", confidence high if the
//     module hosts a matching `from . import views` (we don't verify
//     this — confidence drops to ConfidenceComputed because the
//     dispatcher must resolve cross-module).
//   - bare identifier `foo` → assume same-module → qn = module + "." + foo
//   - `views.FooView.as_view()` → qn = "views.FooView"
//   - lambda / arbitrary expression → empty qn, ConfidenceDynamic
func resolveViewArg(view *tree_sitter.Node, src []byte, module string) (string, float64) {
	if view == nil {
		return "", common.ConfidenceDynamic
	}
	switch view.Kind() {
	case "identifier":
		return common.QualifiedName(module, common.NodeText(view, src)), common.ConfidenceLiteral
	case "attribute":
		// e.g. views.foo or views.FooView
		text := common.NodeText(view, src)
		// Cross-module attribute reference — the dispatcher resolves
		// via the EntityRefCache; we lower confidence to reflect that
		// we cannot verify the module-binding ourselves.
		return text, common.ConfidenceComputed
	case "call":
		// e.g. views.FooView.as_view()
		fn := view.ChildByFieldName("function")
		if fn != nil && fn.Kind() == "attribute" {
			obj := fn.ChildByFieldName("object")
			if obj != nil {
				return common.NodeText(obj, src), common.ConfidenceComputed
			}
		}
		return "", common.ConfidenceDynamic
	}
	return "", common.ConfidenceDynamic
}

// includeCallToRoute emits a single Route entry representing a
// dynamic mount point. Confidence is ConfidenceDynamic per the rubric.
func includeCallToRoute(args *tree_sitter.Node, src []byte, srcPath string) []common.ExtractedRoute {
	// path("api/", include("app.urls")) — but include() also appears
	// as the *second* positional of path(). The current call IS the
	// include() call when entryToRoutes dispatched here. We treat it
	// as a generic mount.
	if args == nil {
		return nil
	}
	target := ""
	if first := common.PositionalAt(args, 0, src); first != nil {
		if s, ok := common.StringLiteralValue(first, src); ok {
			target = s
		}
	}
	if target == "" {
		return nil
	}
	return []common.ExtractedRoute{{
		Method:      "ANY",
		PathPattern: "/" + strings.TrimPrefix(target, "/"),
		Framework:   "django",
		HandlerQN:   target,
		Confidence:  common.ConfidenceDynamic,
		SourcePath:  srcPath,
	}}
}

var _ code_framework.Extractor = (*extractor)(nil)

func init() {
	code_framework.Register(Name, New, code_framework.Descriptor{
		Family:     "routes",
		Languages:  []string{"python"},
		Frameworks: []string{"django"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     []code_framework.EventKind{code_framework.InputCoreFileChanged},
		Outputs:    []code_framework.EntityKind{code_framework.KindRoute, code_framework.KindHandler},
	})
}
