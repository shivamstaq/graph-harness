// Package source_live wraps tree-sitter to extract structural facts from
// Go source files. P0.T18 + P0.T20.
package source_live

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
)

// ParsedFile is the structural extraction output for one source file.
type ParsedFile struct {
	Path      string
	Language  string
	BodyHash  string
	Functions []FunctionDecl
	// TypeDecls carries class/interface declarations recovered from the
	// AST. Tree-sitter parsers emit one entry per syntactic class /
	// interface; the upstream Symbol/Entity projection (extract.
	// ParsedFileToSymbols → code_core.entityFromSymbol) maps them to
	// SymbolKindClass / SymbolKindInterface and the matching EntityKind.
	TypeDecls []TypeDeclDecl
}

// FunctionDecl is one Go function or method extracted from the AST.
type FunctionDecl struct {
	QualifiedName string // pkg.Func or (pkg.Recv).Method
	Receiver      string // empty for plain funcs
	Name          string
	Signature     string // raw signature substring
	StartByte     uint32
	EndByte       uint32
	StartLine     uint32 // 1-based
	EndLine       uint32 // 1-based
	BodyHash      string
}

// TypeDeclKind discriminates the tree-sitter type-declaration variants
// the parsers recover. The string values match source_live.SymbolKind*
// so the upstream symbol projection is a 1:1 rename.
type TypeDeclKind string

// Type-declaration kinds emitted by tree-sitter parsers. Add new
// variants here when adding language-specific syntactic forms (e.g.
// enums); both the Symbol projection and code.core entity formula
// must be updated in lockstep — see SPEC §6.12.
const (
	TypeDeclKindClass     TypeDeclKind = "Class"
	TypeDeclKindInterface TypeDeclKind = "Interface"
)

// TypeDeclDecl is one class or interface extracted from the AST. The
// class/interface itself has no body hash distinct from its source
// span; BodyHash is the sha256 of the declaration span so identical
// declarations across files / commits collapse for the body_hash
// anchor evaluator. Receiver is unused (classes/interfaces ARE the
// receiver) and intentionally omitted.
type TypeDeclDecl struct {
	QualifiedName string
	Name          string
	Kind          TypeDeclKind
	StartByte     uint32
	EndByte       uint32
	StartLine     uint32 // 1-based
	EndLine       uint32 // 1-based
	BodyHash      string
}

// ParseGoFile runs the tree-sitter Go grammar over src and returns a
// ParsedFile. The language id is "go". An empty file produces an empty
// Functions list with no error.
func ParseGoFile(path string, src []byte) (*ParsedFile, error) {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(tree_sitter.NewLanguage(tree_sitter_go.Language())); err != nil {
		return nil, fmt.Errorf("set go language: %w", err)
	}
	tree := parser.Parse(src, nil)
	defer tree.Close()
	root := tree.RootNode()

	pf := &ParsedFile{
		Path:     path,
		Language: "go",
		BodyHash: hashBytes(src),
	}

	pkgName := extractPackage(root, src)
	walk(root, src, func(n *tree_sitter.Node) bool {
		switch n.Kind() {
		case "function_declaration":
			fn := extractFunction(n, src, pkgName, "")
			if fn.Name != "" {
				pf.Functions = append(pf.Functions, fn)
			}
			return false
		case "method_declaration":
			recv := extractMethodReceiver(n, src)
			fn := extractFunction(n, src, pkgName, recv)
			if fn.Name != "" {
				pf.Functions = append(pf.Functions, fn)
			}
			return false
		}
		return true
	})
	return pf, nil
}

func extractPackage(root *tree_sitter.Node, src []byte) string {
	cursor := root.Walk()
	defer cursor.Close()
	for i := uint(0); i < root.ChildCount(); i++ {
		c := root.Child(i)
		if c == nil {
			continue
		}
		if c.Kind() == "package_clause" {
			id := c.ChildByFieldName("name")
			if id != nil {
				return id.Utf8Text(src)
			}
			// Fallback: scan for the first identifier child.
			for j := uint(0); j < c.ChildCount(); j++ {
				cc := c.Child(j)
				if cc != nil && cc.Kind() == "package_identifier" {
					return cc.Utf8Text(src)
				}
			}
		}
	}
	return ""
}

func extractMethodReceiver(n *tree_sitter.Node, src []byte) string {
	recv := n.ChildByFieldName("receiver")
	if recv == nil {
		return ""
	}
	// Receiver shape: (receiver (parameter_declaration name? type)).
	for i := uint(0); i < recv.NamedChildCount(); i++ {
		c := recv.NamedChild(i)
		if c == nil {
			continue
		}
		t := c.ChildByFieldName("type")
		if t == nil {
			t = c.NamedChild(c.NamedChildCount() - 1)
		}
		if t != nil {
			text := t.Utf8Text(src)
			// Strip leading * for pointer receivers.
			if len(text) > 0 && text[0] == '*' {
				text = text[1:]
			}
			return text
		}
	}
	return ""
}

func extractFunction(n *tree_sitter.Node, src []byte, pkg, recv string) FunctionDecl {
	name := ""
	if id := n.ChildByFieldName("name"); id != nil {
		name = id.Utf8Text(src)
	}
	sig := ""
	if params := n.ChildByFieldName("parameters"); params != nil {
		sig = params.Utf8Text(src)
	}
	if rt := n.ChildByFieldName("result"); rt != nil {
		sig += " " + rt.Utf8Text(src)
	}
	body := []byte{}
	if b := n.ChildByFieldName("body"); b != nil {
		body = []byte(b.Utf8Text(src))
	}
	qn := name
	switch {
	case recv != "" && pkg != "":
		qn = pkg + "." + recv + "." + name
	case recv != "":
		qn = recv + "." + name
	case pkg != "":
		qn = pkg + "." + name
	}
	startPos := n.StartPosition()
	endPos := n.EndPosition()
	return FunctionDecl{
		QualifiedName: qn,
		Receiver:      recv,
		Name:          name,
		Signature:     sig,
		StartByte:     uint32(n.StartByte()),               //nolint:gosec
		EndByte:       uint32(n.EndByte()),                 //nolint:gosec
		StartLine:     uint32(startPos.Row+1) & 0xffffffff, //nolint:gosec
		EndLine:       uint32(endPos.Row+1) & 0xffffffff,   //nolint:gosec
		BodyHash:      hashBytes(body),
	}
}

// walk traverses the tree depth-first; visitor returns true to descend.
func walk(n *tree_sitter.Node, src []byte, visit func(*tree_sitter.Node) bool) {
	if !visit(n) {
		return
	}
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c != nil {
			walk(c, src, visit)
		}
	}
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
