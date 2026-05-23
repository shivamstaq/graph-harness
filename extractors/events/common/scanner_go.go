package common

import (
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// GoCallSite is a parsed Go call_expression with the operands the events
// extractors need: the function expression (selector or identifier), the
// argument list, and the byte position used by the function-containing
// walker.
type GoCallSite struct {
	Call     *tree_sitter.Node
	Function *tree_sitter.Node
	Args     []*tree_sitter.Node
	Pos      uint32
}

// FindGoCalls walks root and returns every call_expression whose
// `function` child matches the predicate. predicate receives the
// selector-expression's (recv, field) when the function is of the form
// `recv.field`, or ("", ident) when it's a bare identifier.
//
// Designed for the events.* extractors: each library family has a
// closed set of receiver-method-name pairs it cares about.
func FindGoCalls(root *tree_sitter.Node, src []byte, predicate func(recv, field string) bool) []GoCallSite {
	var out []GoCallSite
	Walk(root, func(n *tree_sitter.Node) bool {
		if n.Kind() != "call_expression" {
			return true
		}
		fn := CallFunctionNode(n)
		if fn == nil {
			return true
		}
		recv, field := "", ""
		switch fn.Kind() {
		case "selector_expression":
			recv, field = GoSelectorIdent(fn, src)
		case "identifier":
			field = fn.Utf8Text(src)
		}
		if !predicate(recv, field) {
			return true
		}
		args := CallArgumentsNode(n)
		out = append(out, GoCallSite{
			Call:     n,
			Function: fn,
			Args:     NamedArguments(args),
			Pos:      uint32(n.StartByte()),
		})
		return true
	})
	return out
}

// GoFirstStringArg returns the first argument that is a Go string
// literal, plus its position in the argument list. Returns ("", -1)
// if no string-literal argument is found.
func GoFirstStringArg(args []*tree_sitter.Node, src []byte) (string, int) {
	for i, a := range args {
		if v, ok := GoStringLiteral(a, src); ok {
			return v, i
		}
	}
	return "", -1
}

// GoCompositeLiteralField returns the value node of the named field
// inside a composite_literal. For
//
//	kafka.Message{Topic: "x"}
//
// `GoCompositeLiteralField(lit, "Topic")` returns the "x" string
// literal node (unwrapped from its literal_element wrapper if present).
// Returns nil if the field is absent or the node is not a
// composite_literal.
//
// Tree-sitter-go layout (as of the bundled grammar version):
//
//	composite_literal
//	  type:   qualified_type | type_identifier | slice_type | ...
//	  body:   literal_value
//	            keyed_element
//	              literal_element  → identifier (key)
//	              literal_element  → expression (value)
//	            literal_element     (positional element, no key)
func GoCompositeLiteralField(lit *tree_sitter.Node, src []byte, fieldName string) *tree_sitter.Node {
	if lit == nil || lit.Kind() != "composite_literal" {
		return nil
	}
	body := lit.ChildByFieldName("body")
	if body == nil {
		for i := uint(0); i < lit.NamedChildCount(); i++ {
			c := lit.NamedChild(i)
			if c != nil && c.Kind() == "literal_value" {
				body = c
				break
			}
		}
	}
	if body == nil {
		return nil
	}
	for i := uint(0); i < body.NamedChildCount(); i++ {
		el := body.NamedChild(i)
		if el == nil || el.Kind() != "keyed_element" {
			continue
		}
		if el.NamedChildCount() < 2 {
			continue
		}
		k := unwrapLiteralElement(el.NamedChild(0))
		v := unwrapLiteralElement(el.NamedChild(1))
		if k == nil || v == nil {
			continue
		}
		if k.Utf8Text(src) == fieldName {
			return v
		}
	}
	return nil
}

// unwrapLiteralElement peels a literal_element wrapper off a node,
// returning the inner expression. Returns the node unchanged if it
// is not a literal_element. Nil-safe.
func unwrapLiteralElement(n *tree_sitter.Node) *tree_sitter.Node {
	if n == nil {
		return nil
	}
	if n.Kind() != "literal_element" {
		return n
	}
	if n.NamedChildCount() == 0 {
		return n
	}
	return n.NamedChild(0)
}

// GoEnclosingQualifiedName looks up the enclosing func / method
// declaration for pos and returns the canonical qualified name in the
// "pkg.Func" or "pkg.Recv.Method" form. Returns "" if pos is at file
// scope.
func GoEnclosingQualifiedName(root *tree_sitter.Node, src []byte, pkg string, pos uint32) string {
	fn := GoFunctionContainingByte(root, pos)
	if fn == nil {
		return ""
	}
	name, recv := GoEnclosingFuncName(fn, src)
	if name == "" {
		return ""
	}
	switch {
	case pkg != "" && recv != "":
		return pkg + "." + recv + "." + name
	case recv != "":
		return recv + "." + name
	case pkg != "":
		return pkg + "." + name
	}
	return name
}
