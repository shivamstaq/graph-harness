// Package chi implements the routes.go.chi framework extractor.
//
// Detects route registration calls against go-chi/chi/v5 routers:
//
//	r.Get("/path", handler)
//	r.Post("/path", handler)
//	r.Put("/path", handler)
//	r.Patch("/path", handler)
//	r.Delete("/path", handler)
//	r.Head("/path", handler)
//	r.Options("/path", handler)
//	r.Connect("/path", handler)
//	r.Trace("/path", handler)
//	r.Handle("/path", handler)
//	r.Method("GET", "/path", handler)
//	r.MethodFunc("GET", "/path", handler)
//	r.Route("/api", func(r chi.Router) { r.Get(...) })   // prefix grouping
//	r.Mount("/api/v2", subrouter)
//	r.Group(func(r chi.Router) { ... })                  // middleware grouping
//
// Per plan/02-framework-extractors.md P2.T06: tree-sitter pattern
// matchers + LSP-fallback handler resolution. Pass 1 ships the
// tree-sitter-only path; LSP-fallback hooks in Pass 2.
//
// Output entity kinds: Route + Handler. Output event kinds: RouteAdded
// + HandlerBound (per internal/code_framework.EmittedEventKinds).
package chi

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
// Used by Register, by Dispatcher.Status output, and by the
// produced_by stamp on emitted events.
const ExtractorName = "routes.go.chi"

// frameworkLabel is the Route.Framework value. Stable string the
// kindwise / framework anchor evaluators (P2.T32) can filter on.
const frameworkLabel = "chi"

// routerMethodVerbs maps chi router-method names to their corresponding
// HTTP verbs. Chi calls these "Methods" on chi.Router (see
// https://pkg.go.dev/github.com/go-chi/chi/v5#Router). Only entries
// here are treated as route registration; "Handle" and "Method" /
// "MethodFunc" carry an explicit verb argument and are handled
// separately.
var routerMethodVerbs = map[string]string{
	"Get":     "GET",
	"Post":    "POST",
	"Put":     "PUT",
	"Patch":   "PATCH",
	"Delete":  "DELETE",
	"Head":    "HEAD",
	"Options": "OPTIONS",
	"Connect": "CONNECT",
	"Trace":   "TRACE",
}

// extractor is the per-workspace chi extractor instance held by the
// Dispatcher across OnEvent calls.
type extractor struct {
	deps code_framework.Deps
}

// New is the package's Constructor. The Dispatcher passes Deps once at
// construction; the same instance handles every OnEvent for the
// workspace lifetime (per code_framework.Extractor godoc).
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
		Frameworks: []string{"chi"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     []code_framework.EventKind{code_framework.InputCoreFileChanged},
		Outputs:    []code_framework.EntityKind{code_framework.KindRoute, code_framework.KindHandler},
	}
}

// Name implements Extractor.
func (e *extractor) Name() string { return ExtractorName }

// Inputs implements Extractor.
func (e *extractor) Inputs() []code_framework.EventKind { return descriptor().Inputs }

// Outputs implements Extractor.
func (e *extractor) Outputs() []code_framework.EntityKind { return descriptor().Outputs }

// Capabilities implements Extractor.
func (e *extractor) Capabilities() code_framework.Capabilities {
	d := descriptor()
	return code_framework.Capabilities{
		Family:     d.Family,
		Languages:  d.Languages,
		Frameworks: d.Frameworks,
		Fallback:   d.Fallback,
		BatchHint:  d.BatchHint,
	}
}

// OnEvent reads the changed file (path from ev.Payload `path` field),
// parses it with tree-sitter-go, and emits one RouteAdded + one
// HandlerBound per detected chi route registration.
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
	if pl.Path == "" {
		return nil, nil
	}
	// Skip non-Go files quickly — the Dispatcher's filter is layer/kind
	// only, so we still see FileChanged events for non-Go files.
	if !strings.HasSuffix(pl.Path, ".go") {
		return nil, nil
	}
	abs := pl.Path
	if !filepath.IsAbs(abs) && e.deps.Workspace != "" {
		abs = filepath.Join(e.deps.Workspace, pl.Path)
	}
	src, err := os.ReadFile(abs)
	if err != nil {
		// Missing file: treat as nothing to extract rather than a
		// hard error so the Dispatcher's error counter doesn't tick
		// on a routine delete that arrived as FileChanged.
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("chi: read %s: %w", pl.Path, err)
	}
	tree, err := common.ParseGoSource(src)
	if err != nil {
		return nil, fmt.Errorf("chi: parse %s: %w", pl.Path, err)
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
		events := e.detectRouteCall(n, src, pkg, pl.Path)
		out = append(out, events...)
		return true
	})
	return out, nil
}

// detectRouteCall inspects one call_expression node to see whether it
// matches one of the chi route-registration patterns. Returns the
// kernel.Event slice to append (empty if no match).
func (e *extractor) detectRouteCall(call *tree_sitter.Node, src []byte, pkg, path string) []kernel.Event {
	fn := common.CallFunctionNode(call)
	if fn == nil || fn.Kind() != "selector_expression" {
		return nil
	}
	_, methodName := common.SelectorIdent(fn, src)
	args := common.NamedArguments(common.CallArgumentsNode(call))

	switch methodName {
	case "Handle":
		// r.Handle("/path", handler)  → verb=ANY
		if len(args) < 2 {
			return nil
		}
		pattern, ok := common.StringLiteralValue(args[0], src)
		if !ok {
			return nil
		}
		return e.buildRoute("ANY", pattern, args[1], src, pkg, path)
	case "Method", "MethodFunc":
		// r.Method("GET", "/path", handler)
		if len(args) < 3 {
			return nil
		}
		verbRaw, vok := common.StringLiteralValue(args[0], src)
		pattern, pok := common.StringLiteralValue(args[1], src)
		if !vok || !pok {
			return nil
		}
		return e.buildRoute(verbRaw, pattern, args[2], src, pkg, path)
	default:
		verb, ok := routerMethodVerbs[methodName]
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
		return e.buildRoute(verb, pattern, args[1], src, pkg, path)
	}
}

func (e *extractor) buildRoute(method, pattern string, handlerArg *tree_sitter.Node, src []byte, pkg, path string) []kernel.Event {
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
		e.deps.Logf("chi: emit route %s %s: %v", method, pattern, err)
	}
	return events
}
