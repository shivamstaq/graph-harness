// Package nethttp implements the routes.go.nethttp framework extractor.
//
// net/http exposes two registration shapes:
//
//	http.HandleFunc("/path", h)
//	http.Handle("/path", h)
//	mux := http.NewServeMux(); mux.HandleFunc("/path", h)
//	mux.Handle("/path", h)
//
// Go 1.22 added per-method patterns:
//
//	mux.HandleFunc("GET /path", h)
//	mux.Handle("POST /path", h)
//
// Per plan/02-framework-extractors.md P2.T06: detect both pre-1.22 and
// 1.22+ patterns. When the pattern carries a leading verb, split it
// into (method, path); otherwise emit method "ANY".
package nethttp

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
const ExtractorName = "routes.go.nethttp"

const frameworkLabel = "net/http"

// httpVerbs is the closed set of canonical HTTP verbs recognized as a
// "GET /path" prefix in Go 1.22+ patterns. Anything else is treated as
// a path that happens to start with an upper-case word and falls
// through to method "ANY". Conservative — we'd rather miss a custom
// verb than mis-attribute a path.
var httpVerbs = map[string]bool{
	"GET":     true,
	"POST":    true,
	"PUT":     true,
	"PATCH":   true,
	"DELETE":  true,
	"HEAD":    true,
	"OPTIONS": true,
	"CONNECT": true,
	"TRACE":   true,
}

// extractor holds per-workspace state for the net/http extractor.
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
		Frameworks: []string{"net/http"},
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

// OnEvent walks the file for HandleFunc / Handle call sites.
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
		return nil, fmt.Errorf("nethttp: read %s: %w", pl.Path, err)
	}
	tree, err := common.ParseGoSource(src)
	if err != nil {
		return nil, fmt.Errorf("nethttp: parse %s: %w", pl.Path, err)
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
	recv, methodName := common.SelectorIdent(fn, src)
	if methodName != "HandleFunc" && methodName != "Handle" {
		return nil
	}
	// Conservative receiver check: accept `http.HandleFunc(...)` (recv
	// == "http") or any other receiver (we can't statically know the
	// type without LSP; the safer call is to accept all ServeMux-shaped
	// receivers because chi/gin/gorilla use different verb-named
	// methods so there's no collision risk in the same call site).
	// Filter out chi-style multi-arg shapes by requiring exactly 2 args.
	args := common.NamedArguments(common.CallArgumentsNode(call))
	if len(args) != 2 {
		return nil
	}
	rawPattern, ok := common.StringLiteralValue(args[0], src)
	if !ok {
		return nil
	}
	method, pattern := splitMethodPattern(rawPattern)
	handlerArg := args[1]
	handlerName := common.HandlerNameFromArg(handlerArg, src, pkg)
	events, err := common.EmitRoute(common.RouteArgs{
		ExtractorName:        ExtractorName,
		Framework:            frameworkLabel,
		Method:               method,
		PathPattern:          pattern,
		HandlerQualifiedName: handlerName,
		HandlerPathGlob:      path,
	})
	if err != nil && e.deps.Logf != nil {
		e.deps.Logf("nethttp: emit route %s %s (recv=%q): %v", method, pattern, recv, err)
	}
	return events
}

// splitMethodPattern handles the Go 1.22+ "VERB /path" mux pattern
// extension. Returns (method, pathOnly). Patterns without a leading
// recognized verb return ("ANY", whole-pattern).
//
// Examples:
//
//	"GET /api/orders"       → ("GET", "/api/orders")
//	"POST /api/orders/{id}" → ("POST", "/api/orders/{id}")
//	"/api/health"           → ("ANY", "/api/health")
//	"GET example.com/api"   → ("GET", "example.com/api")  // host-matching mux pattern
func splitMethodPattern(pattern string) (method, path string) {
	if pattern == "" {
		return "ANY", pattern
	}
	// Split on the FIRST space (1.22 mux requires exactly one).
	idx := strings.Index(pattern, " ")
	if idx <= 0 {
		return "ANY", pattern
	}
	candidate := pattern[:idx]
	rest := pattern[idx+1:]
	if !httpVerbs[candidate] {
		return "ANY", pattern
	}
	return candidate, rest
}
