package common

import (
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// TSCallSite is a parsed TS call_expression with the operands the
// events extractors need.
type TSCallSite struct {
	Call     *tree_sitter.Node
	Function *tree_sitter.Node
	Args     []*tree_sitter.Node
	Pos      uint32
}

// FindTSCalls walks root and returns every call_expression whose
// `function` (`function` field in tree-sitter-typescript) member access
// matches the predicate. predicate receives (object_chain, property)
// where object_chain is the source text of the receiver expression
// (e.g. "kafka.producer()") and property is the final method name.
//
// For bare identifier calls (e.g. `subscribe(...)`) predicate is called
// with ("", "subscribe").
func FindTSCalls(root *tree_sitter.Node, src []byte, predicate func(objectChain, method string) bool) []TSCallSite {
	var out []TSCallSite
	Walk(root, func(n *tree_sitter.Node) bool {
		if n.Kind() != "call_expression" {
			return true
		}
		fn := n.ChildByFieldName("function")
		if fn == nil {
			return true
		}
		obj, method := "", ""
		switch fn.Kind() {
		case "member_expression":
			obj, method = TSMemberAccess(fn, src)
		case "identifier":
			method = fn.Utf8Text(src)
		}
		if !predicate(obj, method) {
			return true
		}
		args := n.ChildByFieldName("arguments")
		out = append(out, TSCallSite{
			Call:     n,
			Function: fn,
			Args:     NamedArguments(args),
			Pos:      uint32(n.StartByte()),
		})
		return true
	})
	return out
}

// TSObjectField returns the value node of the named key inside an
// object expression. Used for shapes like `{ topic: "x" }` /
// `{ groupId: "audit" }`.
//
// Returns nil if obj is not an object expression or the key is absent.
func TSObjectField(obj *tree_sitter.Node, src []byte, key string) *tree_sitter.Node {
	if obj == nil {
		return nil
	}
	if obj.Kind() != "object" && obj.Kind() != "object_expression" {
		return nil
	}
	for i := uint(0); i < obj.NamedChildCount(); i++ {
		c := obj.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Kind() != "pair" {
			continue
		}
		k := c.ChildByFieldName("key")
		v := c.ChildByFieldName("value")
		if k == nil || v == nil {
			continue
		}
		if tsKeyName(k, src) == key {
			return v
		}
	}
	return nil
}

func tsKeyName(k *tree_sitter.Node, src []byte) string {
	switch k.Kind() {
	case "property_identifier", "identifier":
		return k.Utf8Text(src)
	case "string":
		v, _ := TSStringLiteral(k, src)
		return v
	}
	return ""
}

// TSEnclosingQualifiedName walks back from pos to its enclosing function
// / method / arrow-function and returns the canonical name.
//
// Conventions:
//   - For named function declarations: "module.func"
//   - For methods: "module.Class.method"
//   - For arrow functions assigned to const: "module.varName"
//   - For anonymous arrow callbacks inside method calls: "" (fallback)
//
// The module name is derived from the source file's path; the caller
// passes it in (TSModuleName).
func TSEnclosingQualifiedName(root *tree_sitter.Node, src []byte, module string, pos uint32) string {
	fn := TSFunctionContainingByte(root, pos)
	if fn == nil {
		return ""
	}
	// Walk up from fn until we find a named declaration.
	name := tsNameOfDecl(fn, src)
	if name == "" {
		// Try parent chain (e.g. arrow assigned to a variable_declarator).
		p := fn.Parent()
		for p != nil {
			if n := tsNameOfDecl(p, src); n != "" {
				name = n
				break
			}
			// Outside the immediate function context, walk up via parents.
			p = p.Parent()
			if p == nil {
				break
			}
			if p.Kind() == "program" {
				break
			}
		}
	}
	if name == "" {
		return ""
	}
	parts := []string{name}
	// Walk parents looking for enclosing class.
	for p := fn.Parent(); p != nil; p = p.Parent() {
		switch p.Kind() {
		case "class_declaration", "abstract_class_declaration":
			if id := p.ChildByFieldName("name"); id != nil {
				parts = append([]string{id.Utf8Text(src)}, parts...)
			}
		}
	}
	if module != "" {
		parts = append([]string{module}, parts...)
	}
	return strings.Join(parts, ".")
}

func tsNameOfDecl(n *tree_sitter.Node, src []byte) string {
	switch n.Kind() {
	case "function_declaration", "generator_function_declaration":
		if id := n.ChildByFieldName("name"); id != nil {
			return id.Utf8Text(src)
		}
	case "method_definition":
		if id := n.ChildByFieldName("name"); id != nil {
			return id.Utf8Text(src)
		}
	case "variable_declarator":
		if id := n.ChildByFieldName("name"); id != nil {
			return id.Utf8Text(src)
		}
	case "public_field_definition", "field_definition":
		if id := n.ChildByFieldName("name"); id != nil {
			return id.Utf8Text(src)
		}
	}
	return ""
}

// TSModuleName returns the module identifier for a TS file path. Mirrors
// the source_live.tsModuleName convention (basename without extension,
// non-identifier characters replaced with '_').
func TSModuleName(path string) string {
	last := path
	if i := strings.LastIndexAny(path, "/\\"); i >= 0 {
		last = path[i+1:]
	}
	if i := strings.LastIndexByte(last, '.'); i > 0 {
		last = last[:i]
	}
	var b strings.Builder
	b.Grow(len(last))
	for _, r := range last {
		switch {
		case r == '_' || r == '$' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
