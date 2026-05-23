// Package common provides shared helpers for the events.* family of
// framework extractors (Kafka, NATS, AMQP, Redis pub/sub, AWS SNS/SQS)
// across Go, TypeScript, and Python.
//
// Per plan/02-framework-extractors.md P2.T14–T16 + plan/02-implementation-strategy.md
// row "1-Events". Each (transport, language) extractor is a thin Go
// package that walks tree-sitter AST patterns specific to one client
// library family and calls EmitEvent / EmitPublisher / EmitSubscriber
// in this package to build canonical entities.
//
// Cross-language matching (P2.T16): topic strings are compared by exact
// equality across emissions. Each (transport, name) Event row is unique;
// duplicate emissions from different files collapse via MakeContentID
// because the Event payload's anchor + attrs are identical.
//
// Selector anchor convention (per Pass-0.5-A in
// internal/semantic_overlay/anchors/event.go): topic-bearing Event
// entities live in code.core with Kind="Event", QualifiedName=topic_name,
// and KindTag="topic:<transport>". Publishers and subscribers carry
// QualifiedName=event_name with their own Kind. The extractor's
// SelectorRef anchor list embeds these slot values verbatim so the
// resolver in semantic_overlay can rebind during drift.
package common

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Transport string constants for the event-family extractors. Stamped
// onto Event.Transport / EventPublisher.Transport / EventSubscriber.Transport
// and used as the KindTag suffix ("topic:<transport>") on the Event row.
const (
	TransportKafka       = "kafka"
	TransportNATS        = "nats"
	TransportAMQP        = "amqp"
	TransportRedisPubSub = "redis_pubsub"
	TransportSNS         = "sns"
	TransportSQS         = "sqs"
)

// Language IDs match source_live.ParsedFile.Language.
const (
	LanguageGo         = "go"
	LanguageTypeScript = "typescript"
	LanguagePython     = "python"
)

// FileChangedPayload mirrors internal/daemon.FileChangedPayload but is
// re-declared here to avoid importing daemon (which would cycle back
// through code_framework via the dispatcher wiring). Events emitted
// onto the kernel bus by the watcher carry a payload of this shape.
type FileChangedPayload struct {
	Path     string `json:"path"`
	Language string `json:"language,omitempty"`
}

// PathFromEvent pulls the workspace-relative path out of an incoming
// FileChanged / FileRemoved event payload. Returns ("", false) for
// payloads that omit it.
func PathFromEvent(ev kernel.Event) (string, bool) {
	var p FileChangedPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return "", false
	}
	if p.Path == "" {
		return "", false
	}
	return p.Path, true
}

// LanguageForPath returns the source_live language id for a path based
// on its extension. Returns "" for unhandled extensions so the caller
// can bail early without reading the file.
func LanguageForPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return LanguageGo
	case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts":
		return LanguageTypeScript
	case ".py":
		return LanguagePython
	}
	return ""
}

// ParseGoSource returns a tree-sitter tree for Go source. Caller MUST
// defer tree.Close().
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

// ParseTypeScriptSource returns a tree-sitter tree for TS/TSX source.
// Caller MUST defer tree.Close(). Pass the original file path so the
// grammar selector picks TSX for .tsx / .jsx files.
func ParseTypeScriptSource(path string, src []byte) (*tree_sitter.Tree, error) {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	lang := tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript())
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tsx", ".jsx":
		lang = tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTSX())
	}
	if err := parser.SetLanguage(lang); err != nil {
		return nil, fmt.Errorf("set typescript language: %w", err)
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return nil, fmt.Errorf("parse: tree-sitter returned nil tree")
	}
	return tree, nil
}

// ParsePythonSource returns a tree-sitter tree for Python source.
// Caller MUST defer tree.Close().
func ParsePythonSource(src []byte) (*tree_sitter.Tree, error) {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(tree_sitter.NewLanguage(tree_sitter_python.Language())); err != nil {
		return nil, fmt.Errorf("set python language: %w", err)
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return nil, fmt.Errorf("parse: tree-sitter returned nil tree")
	}
	return tree, nil
}

// Walk descends n depth-first; visitor returns true to recurse into
// children, false to skip the subtree.
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

// IdentText returns the utf8 text of an identifier-shaped node, or "".
func IdentText(n *tree_sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "identifier", "field_identifier", "type_identifier",
		"property_identifier", "shorthand_property_identifier",
		"shorthand_property_identifier_pattern":
		return n.Utf8Text(src)
	}
	return ""
}

// GoStringLiteral pulls the value out of a Go interpreted or raw
// string literal node. Returns ("", false) if n is not a string literal.
func GoStringLiteral(n *tree_sitter.Node, src []byte) (string, bool) {
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
	return text[1 : len(text)-1], true
}

// TSStringLiteral pulls the value out of a TS string-shaped node.
// Handles "literal", `template_string` (literal-only, no substitutions),
// and unwraps the leading/trailing quote characters.
//
// Returns ("", false) for non-literal forms (template strings with
// substitutions, computed expressions, identifiers). Per the v1
// limitation documented in extractors/events/README.md, non-literal
// topics are not extracted.
func TSStringLiteral(n *tree_sitter.Node, src []byte) (string, bool) {
	if n == nil {
		return "", false
	}
	switch n.Kind() {
	case "string":
		// Tree-sitter-ts "string" wraps the fragment; the body lives in
		// "string_fragment" children.
		var sb strings.Builder
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			if c.Kind() == "string_fragment" {
				sb.WriteString(c.Utf8Text(src))
			}
		}
		if sb.Len() > 0 {
			return sb.String(), true
		}
		// Fall back to span minus quotes.
		text := n.Utf8Text(src)
		if len(text) < 2 {
			return "", false
		}
		return text[1 : len(text)-1], true
	case "template_string":
		// Reject template strings containing substitutions.
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c != nil && c.Kind() == "template_substitution" {
				return "", false
			}
		}
		text := n.Utf8Text(src)
		if len(text) < 2 {
			return "", false
		}
		return text[1 : len(text)-1], true
	}
	return "", false
}

// PyStringLiteral pulls the value out of a Python string literal node.
// Handles plain strings; rejects f-strings with embedded expressions.
func PyStringLiteral(n *tree_sitter.Node, src []byte) (string, bool) {
	if n == nil || n.Kind() != "string" {
		return "", false
	}
	// Reject f-strings with interpolations.
	hasInterpolation := false
	var sb strings.Builder
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "interpolation":
			hasInterpolation = true
		case "string_content":
			sb.WriteString(c.Utf8Text(src))
		}
	}
	if hasInterpolation {
		return "", false
	}
	if sb.Len() > 0 {
		return sb.String(), true
	}
	// Fall back to span minus quotes (handles older grammars that don't
	// emit string_content children).
	text := n.Utf8Text(src)
	stripped, ok := stripPyQuotes(text)
	if !ok {
		return "", false
	}
	return stripped, true
}

func stripPyQuotes(text string) (string, bool) {
	// Skip optional string prefix (r, b, u, f and combinations).
	i := 0
	for i < len(text) {
		c := text[i]
		if c == 'r' || c == 'R' || c == 'b' || c == 'B' || c == 'u' || c == 'U' || c == 'f' || c == 'F' {
			i++
			continue
		}
		break
	}
	rest := text[i:]
	if len(rest) < 2 {
		return "", false
	}
	if strings.HasPrefix(rest, `"""`) && strings.HasSuffix(rest, `"""`) && len(rest) >= 6 {
		return rest[3 : len(rest)-3], true
	}
	if strings.HasPrefix(rest, `'''`) && strings.HasSuffix(rest, `'''`) && len(rest) >= 6 {
		return rest[3 : len(rest)-3], true
	}
	if (rest[0] == '"' || rest[0] == '\'') && rest[len(rest)-1] == rest[0] {
		return rest[1 : len(rest)-1], true
	}
	return "", false
}

// CallFunctionNode returns the `function` child of a call_expression / call.
func CallFunctionNode(call *tree_sitter.Node) *tree_sitter.Node {
	if call == nil {
		return nil
	}
	return call.ChildByFieldName("function")
}

// CallArgumentsNode returns the arguments child of a call_expression / call.
func CallArgumentsNode(call *tree_sitter.Node) *tree_sitter.Node {
	if call == nil {
		return nil
	}
	return call.ChildByFieldName("arguments")
}

// NamedArguments returns the named children of an argument_list /
// arguments node, preserving source order.
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

// GoSelectorIdent reads a `selector_expression` of the form `recv.Field`
// and returns ("recv", "Field"). For other shapes returns ("", "").
func GoSelectorIdent(n *tree_sitter.Node, src []byte) (recv, field string) {
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

// TSMemberAccess reads a `member_expression` of the form `recv.prop`
// and returns the (object_text, property_name).
func TSMemberAccess(n *tree_sitter.Node, src []byte) (object, property string) {
	if n == nil || n.Kind() != "member_expression" {
		return "", ""
	}
	obj := n.ChildByFieldName("object")
	prop := n.ChildByFieldName("property")
	if obj != nil {
		object = obj.Utf8Text(src)
	}
	if prop != nil {
		property = prop.Utf8Text(src)
	}
	return object, property
}

// PyAttribute reads an `attribute` node of the form `recv.attr` and
// returns (object_text, attribute_name).
func PyAttribute(n *tree_sitter.Node, src []byte) (object, attr string) {
	if n == nil || n.Kind() != "attribute" {
		return "", ""
	}
	obj := n.ChildByFieldName("object")
	att := n.ChildByFieldName("attribute")
	if obj != nil {
		object = obj.Utf8Text(src)
	}
	if att != nil {
		attr = att.Utf8Text(src)
	}
	return object, attr
}

// FunctionContainingByte returns the innermost Go function /
// method declaration whose byte-range contains pos. Returns nil if pos
// is at file scope (e.g. a top-level var init).
func GoFunctionContainingByte(root *tree_sitter.Node, pos uint32) *tree_sitter.Node {
	var best *tree_sitter.Node
	Walk(root, func(n *tree_sitter.Node) bool {
		if n.Kind() != "function_declaration" && n.Kind() != "method_declaration" {
			return true
		}
		startByte := uint32(n.StartByte())
		endByte := uint32(n.EndByte())
		if pos < startByte || pos >= endByte {
			return false
		}
		best = n
		return true
	})
	return best
}

// TSFunctionContainingByte returns the innermost TS function / method /
// arrow-function whose byte-range contains pos.
func TSFunctionContainingByte(root *tree_sitter.Node, pos uint32) *tree_sitter.Node {
	var best *tree_sitter.Node
	Walk(root, func(n *tree_sitter.Node) bool {
		switch n.Kind() {
		case "function_declaration", "method_definition", "arrow_function",
			"function_expression", "generator_function_declaration",
			"generator_function", "function":
			startByte := uint32(n.StartByte())
			endByte := uint32(n.EndByte())
			if pos < startByte || pos >= endByte {
				return false
			}
			best = n
		}
		return true
	})
	return best
}

// PyFunctionContainingByte returns the innermost Python function whose
// byte-range contains pos.
func PyFunctionContainingByte(root *tree_sitter.Node, pos uint32) *tree_sitter.Node {
	var best *tree_sitter.Node
	Walk(root, func(n *tree_sitter.Node) bool {
		if n.Kind() != "function_definition" {
			return true
		}
		startByte := uint32(n.StartByte())
		endByte := uint32(n.EndByte())
		if pos < startByte || pos >= endByte {
			return false
		}
		best = n
		return true
	})
	return best
}

// GoEnclosingFuncName returns the function name + the enclosing
// receiver type. For top-level funcs the receiver is empty.
func GoEnclosingFuncName(fn *tree_sitter.Node, src []byte) (name, recv string) {
	if fn == nil {
		return "", ""
	}
	if id := fn.ChildByFieldName("name"); id != nil {
		name = id.Utf8Text(src)
	}
	if fn.Kind() == "method_declaration" {
		r := fn.ChildByFieldName("receiver")
		if r != nil {
			for i := uint(0); i < r.NamedChildCount(); i++ {
				c := r.NamedChild(i)
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
					recv = text
					break
				}
			}
		}
	}
	return name, recv
}

// GoPackageName extracts the package clause's name from a root node.
func GoPackageName(root *tree_sitter.Node, src []byte) string {
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

// ARNTopicName extracts the topic / queue name from an AWS ARN-style
// string. Returns the last colon- or slash-separated segment so
// "arn:aws:sns:us-east-1:123:order-events" → "order-events" and a bare
// queue URL "https://sqs.us-east-1.amazonaws.com/123/orders" → "orders".
// Returns s unchanged if no separator is present.
func ARNTopicName(s string) string {
	if s == "" {
		return s
	}
	if idx := strings.LastIndexAny(s, ":/"); idx >= 0 && idx < len(s)-1 {
		return s[idx+1:]
	}
	return s
}
