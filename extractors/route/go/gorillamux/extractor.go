// Package gorillamux implements the routes.go.gorillamux framework extractor.
//
// gorilla/mux registers routes with a fluent builder shape:
//
//	r.HandleFunc("/orders/{id}", h).Methods("GET")
//	r.Handle("/health", handler).Methods("GET", "HEAD")
//	r.Path("/orders").Methods("POST").HandlerFunc(h)
//	sub := r.PathPrefix("/api").Subrouter()
//	sub.HandleFunc("/orders", h).Methods("GET")
//
// The "method" of a route is therefore not the called function name
// (HandleFunc / Handle / Path) but the *string literal argument* to a
// chained `.Methods(...)`. The extractor traces the outermost call_expression
// of each registration site and unrolls the receiver chain to recover
// the path pattern + method list.
//
// Per plan/02-framework-extractors.md P2.T06: tree-sitter pattern
// matchers + LSP-fallback handler resolution. Pass 1 ships the
// tree-sitter-only path; LSP-fallback hooks in Pass 2.
package gorillamux

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
const ExtractorName = "routes.go.gorillamux"

const frameworkLabel = "gorilla/mux"

// extractor holds per-workspace state for the gorilla/mux extractor.
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
		Frameworks: []string{"gorilla/mux"},
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

// OnEvent parses the changed file and walks every call_expression to
// find route-registration chains.
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
		return nil, fmt.Errorf("gorillamux: read %s: %w", pl.Path, err)
	}
	tree, err := common.ParseGoSource(src)
	if err != nil {
		return nil, fmt.Errorf("gorillamux: parse %s: %w", pl.Path, err)
	}
	defer tree.Close()
	root := tree.RootNode()
	pkg := common.PackageName(root, src)

	// Walk only the *outermost* call expression in each chain — calling
	// e.collectChain on every nested call_expression would double-count
	// routes. A call is "outermost" when its parent is not also a
	// call_expression's `function` child.
	visited := map[uintptr]bool{}
	var out []kernel.Event
	common.Walk(root, func(n *tree_sitter.Node) bool {
		if err := ctx.Err(); err != nil {
			return false
		}
		if !common.IsCallExpression(n) {
			return true
		}
		// Skip if we already counted this call via its outermost ancestor.
		key := n.Id()
		if visited[key] {
			return true
		}
		// Detect chain origin: outermost call wraps the entire chain.
		if isCallInsideChain(n) {
			return true
		}
		events := e.handleChain(n, src, pkg, pl.Path, visited)
		out = append(out, events...)
		return true
	})
	return out, nil
}

// isCallInsideChain returns true if n is the `function` child's
// selector_expression operand of a parent call_expression. That is, n
// is the receiver of an outer `.Foo(...)` call and so already covered
// by the outer call.
func isCallInsideChain(n *tree_sitter.Node) bool {
	parent := n.Parent()
	if parent == nil {
		return false
	}
	if parent.Kind() != "selector_expression" {
		return false
	}
	grand := parent.Parent()
	if grand == nil || grand.Kind() != "call_expression" {
		return false
	}
	fn := common.CallFunctionNode(grand)
	return fn != nil && fn.Id() == parent.Id()
}

// chainStep captures one call in the fluent chain, e.g.
// `HandleFunc("/orders", h)` or `Methods("GET","POST")`. Order follows
// source order (left-to-right).
type chainStep struct {
	method string             // selector field name (Get, HandleFunc, Methods, …)
	args   []*tree_sitter.Node
}

// handleChain unrolls a complete chain starting from the outermost
// call (e.g. the whole `r.HandleFunc(...).Methods(...)` expression)
// into ordered chainSteps, then synthesizes Route emissions from them.
func (e *extractor) handleChain(outer *tree_sitter.Node, src []byte, pkg, path string, visited map[uintptr]bool) []kernel.Event {
	steps := unrollChain(outer, src, visited)
	if len(steps) == 0 {
		return nil
	}
	// Find the path-bearing step (HandleFunc / Handle / Path / NewRoute)
	// and the verb-bearing step (Methods). The handler argument lives
	// either on HandleFunc/Handle (positional) or as a chained
	// HandlerFunc / Handler call.
	var pattern string
	var handlerArg *tree_sitter.Node
	var verbs []string
	for _, st := range steps {
		switch st.method {
		case "HandleFunc", "Handle":
			if len(st.args) >= 1 {
				if v, ok := common.StringLiteralValue(st.args[0], src); ok {
					pattern = v
				}
			}
			if len(st.args) >= 2 {
				handlerArg = st.args[1]
			}
		case "Path", "PathPrefix":
			// .Path("/x") sets the route path without a handler in
			// this call. Treat as path-bearing only if we have no
			// pattern yet.
			if pattern == "" && len(st.args) >= 1 {
				if v, ok := common.StringLiteralValue(st.args[0], src); ok {
					pattern = v
				}
			}
		case "HandlerFunc", "Handler":
			if handlerArg == nil && len(st.args) >= 1 {
				handlerArg = st.args[0]
			}
		case "Methods":
			for _, a := range st.args {
				if v, ok := common.StringLiteralValue(a, src); ok {
					verbs = append(verbs, v)
				}
			}
		}
	}
	if pattern == "" || handlerArg == nil {
		return nil
	}
	if len(verbs) == 0 {
		// gorilla/mux allows Method-less routes (match any).
		verbs = []string{"ANY"}
	}
	var out []kernel.Event
	handlerName := common.HandlerNameFromArg(handlerArg, src, pkg)
	for _, v := range verbs {
		events, err := common.EmitRoute(common.RouteArgs{
			ExtractorName:        ExtractorName,
			Framework:            frameworkLabel,
			Method:               v,
			PathPattern:          pattern,
			HandlerQualifiedName: handlerName,
			HandlerPathGlob:      path,
		})
		if err != nil && e.deps.Logf != nil {
			e.deps.Logf("gorillamux: emit route %s %s: %v", v, pattern, err)
		}
		out = append(out, events...)
	}
	return out
}

// unrollChain takes the outermost call_expression in a fluent chain
// and returns chainSteps in source order. visited is populated with
// every inner call's ID so the top-level Walk skips them.
//
// Implementation: descend the chain via selector_expression operands.
// Each call's `function` is either a plain identifier (terminal — but
// gorilla chains never bottom out this way; they bottom out at
// `router.Foo`) or a selector_expression whose operand is the next
// inner call_expression.
func unrollChain(outer *tree_sitter.Node, src []byte, visited map[uintptr]bool) []chainStep {
	// Collect calls inside-out, then reverse.
	var inner []*tree_sitter.Node
	for c := outer; c != nil; {
		visited[c.Id()] = true
		inner = append(inner, c)
		fn := common.CallFunctionNode(c)
		if fn == nil || fn.Kind() != "selector_expression" {
			break
		}
		operand := fn.ChildByFieldName("operand")
		if operand == nil || operand.Kind() != "call_expression" {
			break
		}
		c = operand
	}
	// Reverse to source order.
	steps := make([]chainStep, 0, len(inner))
	for i := len(inner) - 1; i >= 0; i-- {
		c := inner[i]
		fn := common.CallFunctionNode(c)
		if fn == nil {
			continue
		}
		method := ""
		switch fn.Kind() {
		case "selector_expression":
			_, method = common.SelectorIdent(fn, src)
		case "identifier":
			method = fn.Utf8Text(src)
		}
		steps = append(steps, chainStep{
			method: method,
			args:   common.NamedArguments(common.CallArgumentsNode(c)),
		})
	}
	return steps
}
