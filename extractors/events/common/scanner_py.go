package common

import (
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// PyCallSite is a parsed Python call with the operands the events
// extractors need. ArgsNode is the raw arguments node so callers that
// need keyword args can iterate through it with src in hand
// (PyKeywordArg).
type PyCallSite struct {
	Call     *tree_sitter.Node
	Function *tree_sitter.Node
	Args     []*tree_sitter.Node // positional-only
	ArgsNode *tree_sitter.Node
	Pos      uint32
}

// FindPyCalls walks root and returns every call whose function child
// matches the predicate. predicate receives (object_text, attribute)
// when the function is of the form `obj.attr`, or ("", ident) when it's
// a bare function call.
func FindPyCalls(root *tree_sitter.Node, src []byte, predicate func(object, attr string) bool) []PyCallSite {
	var out []PyCallSite
	Walk(root, func(n *tree_sitter.Node) bool {
		if n.Kind() != "call" {
			return true
		}
		fn := n.ChildByFieldName("function")
		if fn == nil {
			return true
		}
		obj, attr := "", ""
		switch fn.Kind() {
		case "attribute":
			obj, attr = PyAttribute(fn, src)
		case "identifier":
			attr = fn.Utf8Text(src)
		}
		if !predicate(obj, attr) {
			return true
		}
		args := n.ChildByFieldName("arguments")
		positional := pyPositionalArgs(args)
		out = append(out, PyCallSite{
			Call:     n,
			Function: fn,
			Args:     positional,
			ArgsNode: args,
			Pos:      uint32(n.StartByte()),
		})
		return true
	})
	return out
}

// pyPositionalArgs returns the named children of an arguments node
// that are NOT keyword_argument or dictionary_splat.
func pyPositionalArgs(args *tree_sitter.Node) []*tree_sitter.Node {
	if args == nil {
		return nil
	}
	out := make([]*tree_sitter.Node, 0, args.NamedChildCount())
	for i := uint(0); i < args.NamedChildCount(); i++ {
		c := args.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "keyword_argument", "dictionary_splat", "list_splat":
			continue
		}
		out = append(out, c)
	}
	return out
}

// PyFirstStringArg returns the first positional argument that is a
// string literal, plus its index. Returns ("", -1) if none.
func PyFirstStringArg(args []*tree_sitter.Node, src []byte) (string, int) {
	for i, a := range args {
		if v, ok := PyStringLiteral(a, src); ok {
			return v, i
		}
	}
	return "", -1
}

// PyKeywordArg returns the value node of a keyword argument named key
// in a call's keyword args (passed as the raw args node). Mirrors what
// splitPyArguments tried to build but with access to src.
func PyKeywordArg(callArgs *tree_sitter.Node, src []byte, key string) *tree_sitter.Node {
	if callArgs == nil {
		return nil
	}
	for i := uint(0); i < callArgs.NamedChildCount(); i++ {
		c := callArgs.NamedChild(i)
		if c == nil || c.Kind() != "keyword_argument" {
			continue
		}
		name := c.ChildByFieldName("name")
		value := c.ChildByFieldName("value")
		if name == nil || value == nil {
			continue
		}
		if name.Utf8Text(src) == key {
			return value
		}
	}
	return nil
}

// PyEnclosingQualifiedName returns the qualified name of the function /
// method enclosing pos. Format:
//
//   - top-level def: "module.func"
//   - class method:  "module.Class.method"
//
// The module name is the basename without .py extension; callers pass
// it in (PyModuleName).
func PyEnclosingQualifiedName(root *tree_sitter.Node, src []byte, module string, pos uint32) string {
	fn := PyFunctionContainingByte(root, pos)
	if fn == nil {
		return ""
	}
	name := ""
	if id := fn.ChildByFieldName("name"); id != nil {
		name = id.Utf8Text(src)
	}
	if name == "" {
		return ""
	}
	parts := []string{name}
	for p := fn.Parent(); p != nil; p = p.Parent() {
		if p.Kind() == "class_definition" {
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

// PyModuleName mirrors source_live.pyModuleName: the basename without
// extension.
func PyModuleName(path string) string {
	last := path
	if i := strings.LastIndexAny(path, "/\\"); i >= 0 {
		last = path[i+1:]
	}
	if i := strings.LastIndexByte(last, '.'); i > 0 {
		last = last[:i]
	}
	return last
}

// PyDecoratorArgFirstString walks a decorator's argument list and
// returns the first string literal found. Used by Python frameworks
// that mount handlers via decorators (e.g. nats's `@nc.subscribe("x")`).
func PyDecoratorArgFirstString(call *tree_sitter.Node, src []byte) string {
	if call == nil || call.Kind() != "call" {
		return ""
	}
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return ""
	}
	for i := uint(0); i < args.NamedChildCount(); i++ {
		c := args.NamedChild(i)
		if c == nil {
			continue
		}
		if v, ok := PyStringLiteral(c, src); ok {
			return v
		}
	}
	return ""
}

// PyFunctionDecoratedWith walks the decorators on a decorated_definition
// looking for a call decorator whose attribute matches predicate. Used
// by Python pub/sub libraries where a handler is registered like
// `@app.subscribe("topic")`. Returns the decorator call node (so the
// caller can read its string arg) or nil if no match.
func PyFunctionDecoratedWith(dec *tree_sitter.Node, src []byte, predicate func(object, attr string) bool) *tree_sitter.Node {
	if dec == nil || dec.Kind() != "decorated_definition" {
		return nil
	}
	for i := uint(0); i < dec.NamedChildCount(); i++ {
		c := dec.NamedChild(i)
		if c == nil || c.Kind() != "decorator" {
			continue
		}
		for j := uint(0); j < c.NamedChildCount(); j++ {
			cc := c.NamedChild(j)
			if cc == nil {
				continue
			}
			if cc.Kind() == "call" {
				fn := cc.ChildByFieldName("function")
				if fn == nil {
					continue
				}
				obj, attr := "", ""
				switch fn.Kind() {
				case "attribute":
					obj, attr = PyAttribute(fn, src)
				case "identifier":
					attr = fn.Utf8Text(src)
				}
				if predicate(obj, attr) {
					return cc
				}
			}
		}
	}
	return nil
}

// PyDecoratedDefinitionFunc returns the function_definition child of a
// decorated_definition (the wrapped function whose enclosing-qualified
// name an event extractor wants to anchor against).
func PyDecoratedDefinitionFunc(dec *tree_sitter.Node) *tree_sitter.Node {
	if dec == nil || dec.Kind() != "decorated_definition" {
		return nil
	}
	for i := uint(0); i < dec.NamedChildCount(); i++ {
		c := dec.NamedChild(i)
		if c != nil && c.Kind() == "function_definition" {
			return c
		}
	}
	return nil
}
