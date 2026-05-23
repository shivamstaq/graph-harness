// Package common provides shared helpers for Go HTTP route extractors
// (chi, gin, gorilla/mux, net/http). Per plan/02-implementation-strategy.md
// Pass 1 "1-Routes-Go" agent slice + plan/02-framework-extractors.md
// P2.T06 + P2.T09 — common Route schema across frameworks.
//
// The helpers in this package are deliberately framework-agnostic: each
// framework extractor walks its own AST patterns and calls Emit*Route /
// Emit*Handler to build canonical Route + Handler entities anchored via
// SelectorRef to the handler function in code.core (SPEC §2.2:
// selectors are the only legal cross-layer reference). MakeContentID
// from internal/code_framework guarantees deterministic IDs so re-extract
// over unchanged source emits identical IDs and the Dispatcher's
// compare-before-emit (§6.21) drops them.
package common

import (
	"encoding/json"
	"fmt"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"

	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// ParseGoSource runs the tree-sitter-go grammar over src and returns the
// resulting tree. The caller MUST defer tree.Close().
//
// Each framework extractor parses its own copy of the file because the
// framework-specific pattern matcher walks the AST directly (the shared
// source_live.ParseGoFile flattens to FunctionDecl + TypeDeclDecl, which
// is insufficient for picking up nested call expressions like
// `r.Get("/v1", h)` or `r.HandleFunc(...).Methods("GET")`).
func ParseGoSource(src []byte) (*tree_sitter.Tree, error) {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(tree_sitter.NewLanguage(tree_sitter_go.Language())); err != nil {
		return nil, fmt.Errorf("set go language: %w", err)
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return nil, fmt.Errorf("parse: tree-sitter returned nil tree")
	}
	return tree, nil
}

// Walk descends n depth-first; visitor returns true to recurse into the
// node's children. Mirrors internal/source_live.walk but exported so
// per-framework extractors can share the same shape.
func Walk(n *tree_sitter.Node, visit func(*tree_sitter.Node) bool) {
	if n == nil {
		return
	}
	if !visit(n) {
		return
	}
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c != nil {
			Walk(c, visit)
		}
	}
}

// PackageName extracts the package clause's name from a root node.
// Returns "" if the file has no package clause (e.g. an empty fixture).
func PackageName(root *tree_sitter.Node, src []byte) string {
	for i := uint(0); i < root.ChildCount(); i++ {
		c := root.Child(i)
		if c == nil || c.Kind() != "package_clause" {
			continue
		}
		if id := c.ChildByFieldName("name"); id != nil {
			return id.Utf8Text(src)
		}
		for j := uint(0); j < c.ChildCount(); j++ {
			cc := c.Child(j)
			if cc != nil && cc.Kind() == "package_identifier" {
				return cc.Utf8Text(src)
			}
		}
	}
	return ""
}

// StringLiteralValue strips Go string-literal quoting from a node's
// utf8 text. Handles both interpreted ("...") and raw (`...`) string
// literals. Returns ("", false) if n is not a string literal.
func StringLiteralValue(n *tree_sitter.Node, src []byte) (string, bool) {
	if n == nil {
		return "", false
	}
	if n.Kind() != "interpreted_string_literal" && n.Kind() != "raw_string_literal" {
		return "", false
	}
	text := n.Utf8Text(src)
	if len(text) < 2 {
		return "", false
	}
	// Strip leading/trailing quote (back-tick or double-quote).
	return text[1 : len(text)-1], true
}

// SelectorIdent reads a `selector_expression` of the form `recv.Field`
// and returns ("recv", "Field"). For other shapes returns ("", "").
func SelectorIdent(n *tree_sitter.Node, src []byte) (recv, field string) {
	if n == nil || n.Kind() != "selector_expression" {
		return "", ""
	}
	operand := n.ChildByFieldName("operand")
	fld := n.ChildByFieldName("field")
	if operand != nil {
		recv = operand.Utf8Text(src)
	}
	if fld != nil {
		field = fld.Utf8Text(src)
	}
	return recv, field
}

// FunctionContainingByte walks root, returning the innermost function /
// method declaration whose byte-range contains pos. Returns nil if pos
// is at file scope.
//
// Used by extractors to attribute a route-registration call (e.g.
// `r.Get(...)` inside func `RegisterRoutes(r *chi.Router)`) to its
// surrounding func so the Route's path_glob anchor falls back to the
// enclosing func's qualified_name when no handler arg is detectable.
func FunctionContainingByte(root *tree_sitter.Node, src []byte, pos uint32) *tree_sitter.Node {
	var best *tree_sitter.Node
	Walk(root, func(n *tree_sitter.Node) bool {
		if n.Kind() != "function_declaration" && n.Kind() != "method_declaration" {
			return true
		}
		startByte := uint32(n.StartByte())
		endByte := uint32(n.EndByte())
		if pos < startByte || pos >= endByte {
			return true
		}
		best = n
		return true // keep walking — inner func literal preferred
	})
	return best
}

// FuncName returns the name child of a function_declaration or
// method_declaration node, or "" if none.
func FuncName(n *tree_sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	if id := n.ChildByFieldName("name"); id != nil {
		return id.Utf8Text(src)
	}
	return ""
}

// MethodReceiverType mirrors source_live.extractMethodReceiver: returns
// the receiver type name (without leading '*' for pointer receivers).
// Empty string for plain function declarations.
func MethodReceiverType(n *tree_sitter.Node, src []byte) string {
	if n == nil || n.Kind() != "method_declaration" {
		return ""
	}
	recv := n.ChildByFieldName("receiver")
	if recv == nil {
		return ""
	}
	for i := uint(0); i < recv.NamedChildCount(); i++ {
		c := recv.NamedChild(i)
		if c == nil {
			continue
		}
		t := c.ChildByFieldName("type")
		if t == nil && c.NamedChildCount() > 0 {
			t = c.NamedChild(c.NamedChildCount() - 1)
		}
		if t != nil {
			text := t.Utf8Text(src)
			if len(text) > 0 && text[0] == '*' {
				text = text[1:]
			}
			return text
		}
	}
	return ""
}

// QualifiedNameOf returns "pkg.Func" / "pkg.Recv.Method" for a function
// or method declaration node. Empty pkg falls back to plain name forms.
func QualifiedNameOf(n *tree_sitter.Node, src []byte, pkg string) string {
	if n == nil {
		return ""
	}
	name := FuncName(n, src)
	if name == "" {
		return ""
	}
	recv := MethodReceiverType(n, src)
	switch {
	case recv != "" && pkg != "":
		return pkg + "." + recv + "." + name
	case recv != "":
		return recv + "." + name
	case pkg != "":
		return pkg + "." + name
	}
	return name
}

// HandlerAnchor builds the SelectorRef that pins a route's handler.
// Preference order (per task brief):
//  1. qualified_name (Go-canonical, framework-stable identifier)
//  2. path_glob fallback (anonymous closure — confidence is lower)
//
// Callers MUST NOT mix raw code.core IDs into the anchor list (SPEC §2.2).
func HandlerAnchor(qualifiedName, pathGlob string) code_framework.SelectorRef {
	var anchors []code_framework.Anchor
	if qualifiedName != "" {
		anchors = append(anchors, code_framework.Anchor{Kind: "qualified_name", Value: qualifiedName})
	}
	if pathGlob != "" {
		anchors = append(anchors, code_framework.Anchor{Kind: "path_glob", Value: pathGlob})
	}
	return code_framework.SelectorRef{Anchors: anchors, Unique: qualifiedName != ""}
}

// ConfidenceFor returns the per-(framework, handler-resolution-quality)
// confidence per the task brief:
//   - tree-sitter pattern + named handler  → 0.95
//   - LSP-resolved handler                 → 1.00
//   - anonymous closure (no qualified name) → 0.70
//
// LSP enrichment is not wired in Pass 1 (the Dispatcher does not yet
// hand extractors an LSP handle), so only the 0.95 / 0.70 tiers fire
// in practice. The 1.00 case is reserved for the orchestrator wiring
// in Pass 2 (per implementation strategy "LSP-fallback for handler
// resolution").
func ConfidenceFor(qualifiedName string, viaLSP bool) float64 {
	if viaLSP {
		return 1.00
	}
	if qualifiedName != "" {
		return 0.95
	}
	return 0.70
}

// MethodCanonical uppercases an HTTP method and returns "" for the
// empty input. Centralizes the canonical form so the route_method
// anchor evaluator (internal/semantic_overlay/anchors/route.go) sees
// uniform values regardless of which framework reported the method.
func MethodCanonical(m string) string {
	return strings.ToUpper(strings.TrimSpace(m))
}

// EmitRoute builds the (Route, Handler) pair for a detected route and
// returns the kernel.Event slice to return from OnEvent. The Dispatcher
// stamps Seq/Tx/Layer/ProducedBy before append.
//
// Conventions:
//   - Route entity Kind is the unqualified EntityKind constant ("Route")
//     and the event Kind is "RouteAdded" (matches EmittedEventKinds in
//     internal/code_framework/types.go).
//   - The Route's content-id is built from (Kind, anchor, attrs) so an
//     unchanged source file re-extracted yields identical IDs (§6.21
//     compare-before-emit drops them).
//   - The Handler is emitted as a separate "HandlerBound" event so
//     handler reuse across multiple routes collapses to one Handler row.
func EmitRoute(args RouteArgs) ([]kernel.Event, error) {
	method := MethodCanonical(args.Method)
	if method == "" || args.PathPattern == "" {
		return nil, nil
	}
	anchor := HandlerAnchor(args.HandlerQualifiedName, args.HandlerPathGlob)
	conf := ConfidenceFor(args.HandlerQualifiedName, args.ViaLSP)

	// Route attrs — sorted-key serialization happens inside
	// MakeContentID. The middleware slice is included only when non-
	// empty so a freshly extracted file with no middleware matches a
	// later re-extract with an unchanged shape.
	routeAttrs := map[string]any{
		"method":       method,
		"path_pattern": args.PathPattern,
		"framework":    args.Framework,
	}
	if len(args.Middleware) > 0 {
		routeAttrs["middleware"] = args.Middleware
	}
	if args.ResponseKind != "" {
		routeAttrs["response_kind"] = args.ResponseKind
	}
	routeID := code_framework.MakeContentID(code_framework.KindRoute, anchor, routeAttrs)

	route := code_framework.Route{
		ID:           routeID,
		Kind:         code_framework.KindRoute,
		Method:       method,
		PathPattern:  args.PathPattern,
		Framework:    args.Framework,
		Middleware:   args.Middleware,
		ResponseKind: args.ResponseKind,
		AnchoredTo:   anchor,
		Provenance: code_framework.Provenance{
			Confidence:  conf,
			Freshness:   kernel.FreshnessLive,
			SourceClass: []kernel.SourceClass{code_framework.SourceExtractorFramework},
			ProducedBy:  "extractor:framework:" + args.ExtractorName,
			Inputs:      args.InputRefs,
		},
	}

	hsum := anchor.Hash()
	handlerAttrs := map[string]any{
		"handler_anchor": fmt.Sprintf("%x", hsum[:]),
	}
	handlerID := code_framework.MakeContentID(code_framework.KindHandler, anchor, handlerAttrs)
	handler := code_framework.Handler{
		ID:         handlerID,
		Kind:       code_framework.KindHandler,
		AnchoredTo: anchor,
		Provenance: code_framework.Provenance{
			Confidence:  conf,
			Freshness:   kernel.FreshnessLive,
			SourceClass: []kernel.SourceClass{code_framework.SourceExtractorFramework},
			ProducedBy:  "extractor:framework:" + args.ExtractorName,
			Inputs:      args.InputRefs,
		},
	}

	rPayload, err := json.Marshal(route)
	if err != nil {
		return nil, fmt.Errorf("marshal route: %w", err)
	}
	hPayload, err := json.Marshal(handler)
	if err != nil {
		return nil, fmt.Errorf("marshal handler: %w", err)
	}
	return []kernel.Event{
		{Kind: "RouteAdded", Payload: rPayload},
		{Kind: "HandlerBound", Payload: hPayload},
	}, nil
}

// RouteArgs is the argument bundle passed by per-framework extractors
// to EmitRoute. Frames the call so the per-framework code paths read
// declaratively (no positional argument surprises across four package
// init files).
type RouteArgs struct {
	// ExtractorName is the registered name (e.g. "routes.go.chi"). The
	// produced_by stamp is "extractor:framework:" + ExtractorName.
	ExtractorName string
	// Framework records the per-extractor framework label
	// (e.g. "chi"). Carried verbatim on Route.Framework so the kindwise
	// + framework anchor selectors can target a specific framework.
	Framework string
	// Method is the HTTP verb the route accepts; canonicalized to upper
	// case inside EmitRoute.
	Method string
	// PathPattern is the route's path expression as the framework
	// itself records it (e.g. "/v1/orders/{id}" for chi, "/orders/:id"
	// for gin).
	PathPattern string
	// HandlerQualifiedName is the "pkg.Func" / "pkg.Recv.Method" form
	// of the resolved handler. Empty for anonymous closures (the
	// confidence drops to 0.7 in that case).
	HandlerQualifiedName string
	// HandlerPathGlob is the source-path glob used as the secondary
	// anchor for anonymous closures (so the resolver can re-locate the
	// closure across edits in the same file).
	HandlerPathGlob string
	// Middleware names the middleware functions the framework chains
	// in front of this route, in declaration order.
	Middleware []string
	// ResponseKind is an optional response-shape hint (e.g. "json")
	// extracted when the handler body is statically obvious. Empty if
	// not inferable.
	ResponseKind string
	// ViaLSP signals that the handler ref was resolved via LSP (not
	// tree-sitter alone), bumping confidence to 1.00.
	ViaLSP bool
	// InputRefs is the upstream event/entity ids feeding this fact.
	// Plumbed verbatim into Provenance.Inputs so downstream consumers
	// can trace the causal chain.
	InputRefs []string
}

// IsCallExpression returns true iff n is an AST call_expression.
func IsCallExpression(n *tree_sitter.Node) bool {
	return n != nil && n.Kind() == "call_expression"
}

// CallFunctionNode returns the `function` child of a call_expression,
// or nil. Centralized so per-framework code paths don't reach into
// tree-sitter field names directly.
func CallFunctionNode(call *tree_sitter.Node) *tree_sitter.Node {
	if call == nil {
		return nil
	}
	return call.ChildByFieldName("function")
}

// CallArgumentsNode returns the `arguments` child of a call_expression,
// or nil.
func CallArgumentsNode(call *tree_sitter.Node) *tree_sitter.Node {
	if call == nil {
		return nil
	}
	return call.ChildByFieldName("arguments")
}

// NamedArguments returns the named (non-comma) children of an
// argument_list node, preserving source order.
func NamedArguments(args *tree_sitter.Node) []*tree_sitter.Node {
	if args == nil {
		return nil
	}
	out := make([]*tree_sitter.Node, 0, args.NamedChildCount())
	for i := uint(0); i < args.NamedChildCount(); i++ {
		c := args.NamedChild(i)
		if c != nil {
			out = append(out, c)
		}
	}
	return out
}

// IdentText returns the utf8 text of an identifier-shaped node
// ("identifier", "field_identifier", "type_identifier"), or "".
func IdentText(n *tree_sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "identifier", "field_identifier", "type_identifier":
		return n.Utf8Text(src)
	}
	return ""
}

// HandlerNameFromArg recovers a "pkg.Func" / "Recv.Method" form from
// an argument expression that names a Go handler. Supports:
//
//   - `bareIdent`              → "<pkg>.bareIdent" if pkg is non-empty
//   - `recv.Field`             → "recv.Field" (selector expression)
//   - func literal             → "" (anonymous; caller falls back to
//     path_glob anchor)
//   - any other shape          → ""
//
// Returns the recovered qualified name; an empty string means the
// caller should treat the handler as anonymous.
func HandlerNameFromArg(n *tree_sitter.Node, src []byte, pkg string) string {
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "identifier":
		name := n.Utf8Text(src)
		if pkg != "" {
			return pkg + "." + name
		}
		return name
	case "selector_expression":
		recv, field := SelectorIdent(n, src)
		if recv != "" && field != "" {
			return recv + "." + field
		}
		return ""
	case "func_literal":
		return ""
	}
	return ""
}
