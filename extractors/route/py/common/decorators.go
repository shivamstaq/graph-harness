package common

import (
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// DecoratedFunc captures a `decorated_definition` AST node along with
// its decorators and the underlying `function_definition`. Extractors
// inspect Decorators to detect framework patterns.
type DecoratedFunc struct {
	Node      *tree_sitter.Node // the decorated_definition node
	FuncDef   *tree_sitter.Node // the inner function_definition
	FuncName  string
	Decorators []*tree_sitter.Node // each `decorator` child
}

// WalkDecoratedFuncs walks the tree rooted at `root` and yields every
// `decorated_definition` whose inner definition is a function_definition.
// classCtx is the enclosing class context (empty at module scope);
// callback receives the dotted qualified-name prefix so methods get
// the right Module.Class.method form.
//
// The callback receives the qualified-name *prefix* (everything up to
// but not including the function name). Inside class bodies it's
// "Class"; at module scope it's "".
func WalkDecoratedFuncs(root *tree_sitter.Node, src []byte, cb func(prefix string, df DecoratedFunc)) {
	walkDecorated(root, src, "", cb)
}

func walkDecorated(n *tree_sitter.Node, src []byte, classCtx string, cb func(string, DecoratedFunc)) {
	if n == nil {
		return
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "decorated_definition":
			def := c.ChildByFieldName("definition")
			if def == nil {
				for j := uint(0); j < c.NamedChildCount(); j++ {
					cc := c.NamedChild(j)
					if cc == nil {
						continue
					}
					if cc.Kind() == "function_definition" || cc.Kind() == "class_definition" {
						def = cc
						break
					}
				}
			}
			if def == nil {
				continue
			}
			if def.Kind() == "function_definition" {
				name := NodeText(def.ChildByFieldName("name"), src)
				cb(classCtx, DecoratedFunc{
					Node:       c,
					FuncDef:    def,
					FuncName:   name,
					Decorators: collectDecorators(c),
				})
			} else if def.Kind() == "class_definition" {
				className := NodeText(def.ChildByFieldName("name"), src)
				body := def.ChildByFieldName("body")
				if body != nil {
					walkDecorated(body, src, joinDotted(classCtx, className), cb)
				}
			}
		case "function_definition":
			// Plain function — no decorators, skip (caller only cares
			// about decorated funcs here).
			_ = c
		case "class_definition":
			name := NodeText(c.ChildByFieldName("name"), src)
			body := c.ChildByFieldName("body")
			if body != nil {
				walkDecorated(body, src, joinDotted(classCtx, name), cb)
			}
		default:
			// Recurse into block-like nodes (if/try/with) so route
			// decorators inside conditional registration still surface.
			if c.NamedChildCount() > 0 {
				walkDecorated(c, src, classCtx, cb)
			}
		}
	}
}

func collectDecorators(decorated *tree_sitter.Node) []*tree_sitter.Node {
	var out []*tree_sitter.Node
	for i := uint(0); i < decorated.NamedChildCount(); i++ {
		c := decorated.NamedChild(i)
		if c != nil && c.Kind() == "decorator" {
			out = append(out, c)
		}
	}
	return out
}

func joinDotted(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "." + b
}

// DecoratorCall holds the parsed shape of a decorator like
// `@app.get("/path", response_model=Foo)`. Receiver is "app",
// Attr is "get", Args is the unparsed argument-list AST node.
type DecoratorCall struct {
	Receiver string
	Attr     string
	Args     *tree_sitter.Node // `argument_list` node, may be nil
}

// ParseDecorator unpacks a `decorator` AST node. Returns ("", "", nil)
// for shapes the extractor doesn't recognize (e.g. bare `@cached`
// without a call, or `@module.submodule.fn` chains deeper than one
// level).
func ParseDecorator(dec *tree_sitter.Node, src []byte) DecoratorCall {
	if dec == nil {
		return DecoratorCall{}
	}
	// The decorator's first named child is the expression after `@`.
	// It's either:
	//   - call (with `function` field = attribute / identifier)
	//   - attribute (bare @app.foo, no call)
	//   - identifier (@cached)
	var expr *tree_sitter.Node
	for i := uint(0); i < dec.NamedChildCount(); i++ {
		c := dec.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Kind() == "comment" {
			continue
		}
		expr = c
		break
	}
	if expr == nil {
		return DecoratorCall{}
	}
	var dc DecoratorCall
	switch expr.Kind() {
	case "call":
		fn := expr.ChildByFieldName("function")
		dc.Args = expr.ChildByFieldName("arguments")
		dc.Receiver, dc.Attr = parseFnTarget(fn, src)
	case "attribute":
		dc.Receiver, dc.Attr = parseAttribute(expr, src)
	case "identifier":
		dc.Attr = NodeText(expr, src)
	}
	return dc
}

func parseFnTarget(fn *tree_sitter.Node, src []byte) (recv, attr string) {
	if fn == nil {
		return "", ""
	}
	switch fn.Kind() {
	case "attribute":
		return parseAttribute(fn, src)
	case "identifier":
		return "", NodeText(fn, src)
	}
	return "", ""
}

func parseAttribute(n *tree_sitter.Node, src []byte) (recv, attr string) {
	if n == nil {
		return "", ""
	}
	obj := n.ChildByFieldName("object")
	at := n.ChildByFieldName("attribute")
	return NodeText(obj, src), NodeText(at, src)
}

// PositionalString returns the first positional argument of an
// argument_list as a literal string, if it is one. Returns ("", false)
// if the list is nil, empty, or the first arg is not a string literal.
func PositionalString(args *tree_sitter.Node, src []byte) (string, bool) {
	if args == nil {
		return "", false
	}
	for i := uint(0); i < args.NamedChildCount(); i++ {
		c := args.NamedChild(i)
		if c == nil {
			continue
		}
		// Skip keyword_argument entries — we want the first positional.
		if c.Kind() == "keyword_argument" {
			continue
		}
		return StringLiteralValue(c, src)
	}
	return "", false
}

// PositionalAt returns the i-th positional argument as an AST node,
// skipping keyword_arguments. Returns nil if the index is out of range.
func PositionalAt(args *tree_sitter.Node, idx int, src []byte) *tree_sitter.Node {
	if args == nil {
		return nil
	}
	pos := 0
	for i := uint(0); i < args.NamedChildCount(); i++ {
		c := args.NamedChild(i)
		if c == nil || c.Kind() == "keyword_argument" {
			continue
		}
		if pos == idx {
			return c
		}
		pos++
	}
	return nil
}

// KeywordArg returns the value AST node for the keyword argument
// `name` in an argument_list. Returns nil if not found.
func KeywordArg(args *tree_sitter.Node, name string, src []byte) *tree_sitter.Node {
	if args == nil {
		return nil
	}
	for i := uint(0); i < args.NamedChildCount(); i++ {
		c := args.NamedChild(i)
		if c == nil || c.Kind() != "keyword_argument" {
			continue
		}
		keyNode := c.ChildByFieldName("name")
		if keyNode == nil {
			continue
		}
		if NodeText(keyNode, src) == name {
			return c.ChildByFieldName("value")
		}
	}
	return nil
}

// ListLiteralStrings extracts string-literal entries from a `list`
// AST node. Used to parse `methods=['GET', 'POST']` in Flask. Non-
// literal entries are skipped silently.
func ListLiteralStrings(n *tree_sitter.Node, src []byte) []string {
	if n == nil || (n.Kind() != "list" && n.Kind() != "tuple") {
		return nil
	}
	var out []string
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		if s, ok := StringLiteralValue(c, src); ok {
			out = append(out, strings.ToUpper(s))
		}
	}
	return out
}
