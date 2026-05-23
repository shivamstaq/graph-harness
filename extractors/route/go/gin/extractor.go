// Package gin implements the routes.go.gin framework extractor.
//
// gin (gin-gonic/gin) registers routes via per-verb methods on a
// gin.IRouter:
//
//	r.GET("/orders", handler)
//	r.POST("/orders", handler)
//	r.PUT("/orders/:id", handler)
//	r.PATCH("/orders/:id", handler)
//	r.DELETE("/orders/:id", handler)
//	r.HEAD("/orders", handler)
//	r.OPTIONS("/orders", handler)
//	r.Any("/wild", handler)                    // matches every verb
//	r.Handle("CUSTOM", "/path", handler)       // verb is positional
//	api := r.Group("/api")
//	api.GET("/orders", handler)
//
// Per plan/02-framework-extractors.md P2.T06: tree-sitter pattern
// matchers + LSP-fallback handler resolution.
package gin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/shivamstaq/graph-harness/extractors/route/go/common"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// ExtractorName is the canonical registry name for this extractor.
const ExtractorName = "routes.go.gin"

const frameworkLabel = "gin"

// verbMethods is the closed set of gin per-verb router methods we
// recognize. .Any expands into "ANY". .Handle takes a positional verb
// (handled separately). Mixed-case method names are gin's own
// convention (always upper-case).
var verbMethods = map[string]string{
	"GET":     "GET",
	"POST":    "POST",
	"PUT":     "PUT",
	"PATCH":   "PATCH",
	"DELETE":  "DELETE",
	"HEAD":    "HEAD",
	"OPTIONS": "OPTIONS",
	"Any":     "ANY",
}

// extractor holds per-workspace state for the gin extractor.
type extractor struct {
	deps code_framework.Deps
}

// New is the Constructor.
func New(deps code_framework.Deps) (code_framework.Extractor, error) {
	return &extractor{deps: deps}, nil
}

func init() {
	code_framework.Register(ExtractorName, New, descriptor())
}

func descriptor() code_framework.Descriptor {
	return code_framework.Descriptor{
		Name:       ExtractorName,
		Family:     "routes",
		Languages:  []string{"go"},
		Frameworks: []string{"gin"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     []code_framework.EventKind{code_framework.InputCoreFileChanged},
		Outputs:    []code_framework.EntityKind{code_framework.KindRoute, code_framework.KindHandler},
	}
}

func (e *extractor) Name() string                          { return ExtractorName }
func (e *extractor) Inputs() []code_framework.EventKind    { return descriptor().Inputs }
func (e *extractor) Outputs() []code_framework.EntityKind  { return descriptor().Outputs }
func (e *extractor) Capabilities() code_framework.Capabilities {
	d := descriptor()
	return code_framework.Capabilities{
		Family: d.Family, Languages: d.Languages, Frameworks: d.Frameworks,
		Fallback: d.Fallback, BatchHint: d.BatchHint,
	}
}

// OnEvent walks the file's AST for gin route-registration calls.
func (e *extractor) OnEvent(ctx context.Context, ev kernel.Event) ([]kernel.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var pl struct {
		Path     string `json:"path"`
		Language string `json:"language,omitempty"`
	}
	if len(ev.Payload) > 0 {
		_ = json.Unmarshal(ev.Payload, &pl)
	}
	if pl.Path == "" || !strings.HasSuffix(pl.Path, ".go") {
		return nil, nil
	}
	abs := pl.Path
	if !filepath.IsAbs(abs) && e.deps.Workspace != "" {
		abs = filepath.Join(e.deps.Workspace, pl.Path)
	}
	src, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("gin: read %s: %w", pl.Path, err)
	}
	tree, err := common.ParseGoSource(src)
	if err != nil {
		return nil, fmt.Errorf("gin: parse %s: %w", pl.Path, err)
	}
	defer tree.Close()
	root := tree.RootNode()
	pkg := common.PackageName(root, src)

	var out []kernel.Event
	common.Walk(root, func(n *tree_sitter.Node) bool {
		if err := ctx.Err(); err != nil {
			return false
		}
		if !common.IsCallExpression(n) {
			return true
		}
		events := e.detectCall(n, src, pkg, pl.Path)
		out = append(out, events...)
		return true
	})
	return out, nil
}

func (e *extractor) detectCall(call *tree_sitter.Node, src []byte, pkg, path string) []kernel.Event {
	fn := common.CallFunctionNode(call)
	if fn == nil || fn.Kind() != "selector_expression" {
		return nil
	}
	_, methodName := common.SelectorIdent(fn, src)
	args := common.NamedArguments(common.CallArgumentsNode(call))

	switch methodName {
	case "Handle":
		// r.Handle("CUSTOM", "/path", handler, ...)
		if len(args) < 3 {
			return nil
		}
		verb, vok := common.StringLiteralValue(args[0], src)
		pattern, pok := common.StringLiteralValue(args[1], src)
		if !vok || !pok {
			return nil
		}
		// Last arg is the handler (gin allows middleware chain
		// preceding the handler: r.Handle("GET", "/p", mw, mw, h)).
		handler := args[len(args)-1]
		return e.emit(verb, pattern, handler, src, pkg, path, middlewareArgs(args[2:len(args)-1], src))
	default:
		verb, ok := verbMethods[methodName]
		if !ok {
			return nil
		}
		if len(args) < 2 {
			return nil
		}
		pattern, pok := common.StringLiteralValue(args[0], src)
		if !pok {
			return nil
		}
		handler := args[len(args)-1]
		return e.emit(verb, pattern, handler, src, pkg, path, middlewareArgs(args[1:len(args)-1], src))
	}
}

func (e *extractor) emit(method, pattern string, handler *tree_sitter.Node, src []byte, pkg, path string, mw []string) []kernel.Event {
	handlerName := common.HandlerNameFromArg(handler, src, pkg)
	events, err := common.EmitRoute(common.RouteArgs{
		ExtractorName:        ExtractorName,
		Framework:            frameworkLabel,
		Method:               method,
		PathPattern:          pattern,
		HandlerQualifiedName: handlerName,
		HandlerPathGlob:      path,
		Middleware:           mw,
	})
	if err != nil && e.deps.Logf != nil {
		e.deps.Logf("gin: emit route %s %s: %v", method, pattern, err)
	}
	return events
}

// middlewareArgs renders middleware-arg nodes as their qualified name
// or "" placeholder (for anonymous func literals). Sort-stable order
// is preserved so re-extract over unchanged source produces an
// identical Middleware slice.
func middlewareArgs(args []*tree_sitter.Node, src []byte) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, 0, len(args))
	for _, a := range args {
		name := common.HandlerNameFromArg(a, src, "")
		if name == "" {
			name = "<anonymous>"
		}
		out = append(out, name)
	}
	return out
}
