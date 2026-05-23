// Package flask implements the Flask HTTP-route extractor (P2.T08 /
// Pass 1 1-Routes-Py). It detects `@app.route("/path", methods=[...])`
// and `@blueprint.route("/path", methods=[...])` decorators.
//
// Limitations (documented in extractors/route/py/README.md):
//   - Class-based views via `MethodView.as_view` are detected only when
//     the assignment site is in the same file as the class definition.
//     Cross-file `add_url_rule(...)` registrations resolve at
//     confidence 0.7.
//   - Blueprint `url_prefix` is honored when it appears as a
//     literal kwarg on the `Blueprint(...)` constructor in the same
//     module; cross-module mounting is v2.
package flask

import (
	"context"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/shivamstaq/graph-harness/extractors/route/py/common"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Name is the registry name for this extractor.
const Name = "routes.py.flask"

type extractor struct {
	deps code_framework.Deps
}

// New builds a Flask route extractor.
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
		Frameworks: []string{"flask"},
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

// detect walks the parsed tree and returns one ExtractedRoute per
// `@<recv>.route("/path", methods=[...])` decorator. Blueprint
// url_prefix kwargs are honored when the Blueprint is constructed in
// the same module.
func (e *extractor) detect(root *tree_sitter.Node, src []byte, path string) []common.ExtractedRoute {
	module := common.ModuleName(path)
	prefixes := collectBlueprintPrefixes(root, src)

	var routes []common.ExtractedRoute
	common.WalkDecoratedFuncs(root, src, func(classCtx string, df common.DecoratedFunc) {
		for _, dec := range df.Decorators {
			rs := e.routesFromDecorator(dec, src, module, classCtx, df.FuncName, prefixes, path)
			routes = append(routes, rs...)
		}
	})
	return routes
}

// collectBlueprintPrefixes maps a module-level variable name to its
// `url_prefix` kwarg if any. `app = Flask(__name__)` resolves to
// prefix="" so the receiver still matches the prefixes map.
func collectBlueprintPrefixes(root *tree_sitter.Node, src []byte) map[string]string {
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
		if right.Kind() != "call" {
			continue
		}
		fn := right.ChildByFieldName("function")
		fnName := common.NodeText(fn, src)
		shortName := fnName
		if idx := strings.LastIndex(shortName, "."); idx >= 0 {
			shortName = shortName[idx+1:]
		}
		if shortName != "Blueprint" && shortName != "Flask" {
			continue
		}
		args := right.ChildByFieldName("arguments")
		prefixNode := common.KeywordArg(args, "url_prefix", src)
		if prefixNode == nil {
			out[varName] = ""
			continue
		}
		if prefix, ok := common.StringLiteralValue(prefixNode, src); ok {
			out[varName] = prefix
		} else {
			out[varName] = ""
		}
	}
	return out
}

// routesFromDecorator returns one ExtractedRoute per method declared
// in the decorator's methods= kwarg. Default methods=[GET] applies
// when the kwarg is missing or non-literal.
func (e *extractor) routesFromDecorator(
	dec *tree_sitter.Node,
	src []byte,
	module, classCtx, fnName string,
	prefixes map[string]string,
	path string,
) []common.ExtractedRoute {
	dc := common.ParseDecorator(dec, src)
	if dc.Attr != "route" {
		return nil
	}
	pathPattern, literal := common.PositionalString(dc.Args, src)
	confidence := common.ConfidenceLiteral
	if !literal {
		pathPattern = "<computed>"
		confidence = common.ConfidenceComputed
	}

	prefix := prefixes[dc.Receiver]
	full := common.JoinPath(prefix, pathPattern)

	// methods= kwarg controls allowed verbs.
	methods := []string{"GET"}
	if mv := common.KeywordArg(dc.Args, "methods", src); mv != nil {
		if got := common.ListLiteralStrings(mv, src); len(got) > 0 {
			methods = got
		} else {
			// Non-literal methods list — drop to computed confidence.
			confidence = common.ConfidenceComputed
		}
	}

	qn := common.QualifiedName(common.QualifiedName(module, classCtx), fnName)
	out := make([]common.ExtractedRoute, 0, len(methods))
	for _, m := range methods {
		out = append(out, common.ExtractedRoute{
			Method:      m,
			PathPattern: full,
			Framework:   "flask",
			HandlerQN:   qn,
			Confidence:  confidence,
			SourcePath:  path,
		})
	}
	return out
}

var _ code_framework.Extractor = (*extractor)(nil)

func init() {
	code_framework.Register(Name, New, code_framework.Descriptor{
		Family:     "routes",
		Languages:  []string{"python"},
		Frameworks: []string{"flask"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     []code_framework.EventKind{code_framework.InputCoreFileChanged},
		Outputs:    []code_framework.EntityKind{code_framework.KindRoute, code_framework.KindHandler},
	})
}
