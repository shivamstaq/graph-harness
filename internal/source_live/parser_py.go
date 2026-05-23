package source_live

import (
	"fmt"
	"path/filepath"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
)

// ParsePythonFile runs the tree-sitter Python grammar over src and
// returns a ParsedFile. Language id is "python". Class methods are
// emitted with Receiver set to the enclosing class name(s); top-level
// functions emit Receiver="".
//
// Tolerant of parse errors per SPEC §6.11. Decorators, async, and
// PEP 695 type-parameter syntax are all surfaced — unsupported syntax
// degrades to a lower-confidence FunctionDecl rather than crashing.
func ParsePythonFile(path string, src []byte) (*ParsedFile, error) {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(tree_sitter.NewLanguage(tree_sitter_python.Language())); err != nil {
		return nil, fmt.Errorf("set python language: %w", err)
	}
	tree := parser.Parse(src, nil)
	defer tree.Close()
	root := tree.RootNode()

	pf := &ParsedFile{
		Path:     path,
		Language: "python",
		BodyHash: hashBytes(src),
	}

	moduleName := pyModuleName(path)
	pyCollect(root, src, moduleName, "", pf)
	return pf, nil
}

// pyCollect walks a Python AST gathering function and method
// declarations. classCtx is the dotted path of enclosing classes (used
// both as Receiver for methods and as the qualified-name prefix).
func pyCollect(n *tree_sitter.Node, src []byte, module, classCtx string, pf *ParsedFile) {
	if n == nil {
		return
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "function_definition":
			fn := pyExtractFunction(c, src, module, classCtx)
			if fn.Name != "" {
				pf.Functions = append(pf.Functions, fn)
			}
		case "decorated_definition":
			pyHandleDecorated(c, src, module, classCtx, pf)
		case "class_definition":
			pyHandleClass(c, src, module, classCtx, pf)
		}
	}
}

// pyHandleClass extracts a class's methods (recursively, since Python
// allows nested classes) and recurses to keep top-level functions
// emitted from any nested function bodies. Also emits the class itself
// as a TypeDeclDecl — Python has no separate Interface concept, so all
// type declarations carry TypeDeclKindClass.
func pyHandleClass(class *tree_sitter.Node, src []byte, module, classCtx string, pf *ParsedFile) {
	name := pyNamedField(class, "name", src)
	qn := pyQualifiedName(module, classCtx, name)
	pf.TypeDecls = append(pf.TypeDecls, pyExtractTypeDecl(class, src, qn, name))
	body := class.ChildByFieldName("body")
	if body == nil {
		return
	}
	pyCollect(body, src, module, joinDotted(classCtx, name), pf)
}

// pyExtractTypeDecl builds a TypeDeclDecl for a class_definition node.
// BodyHash hashes the full declaration span — see tsExtractTypeDecl for
// the rationale.
func pyExtractTypeDecl(n *tree_sitter.Node, src []byte, qn, name string) TypeDeclDecl {
	startPos := n.StartPosition()
	endPos := n.EndPosition()
	bytes := n.Utf8Text(src)
	return TypeDeclDecl{
		QualifiedName: qn,
		Name:          name,
		Kind:          TypeDeclKindClass,
		StartByte:     uint32(n.StartByte()),               //nolint:gosec
		EndByte:       uint32(n.EndByte()),                 //nolint:gosec
		StartLine:     uint32(startPos.Row+1) & 0xffffffff, //nolint:gosec
		EndLine:       uint32(endPos.Row+1) & 0xffffffff,   //nolint:gosec
		BodyHash:      hashBytes([]byte(bytes)),
	}
}

// pyHandleDecorated unwraps a decorated_definition (function or class)
// so callers see through the decorator wrapping.
func pyHandleDecorated(d *tree_sitter.Node, src []byte, module, classCtx string, pf *ParsedFile) {
	def := d.ChildByFieldName("definition")
	if def == nil {
		// Fallback: scan named children for the wrapped node.
		for i := uint(0); i < d.NamedChildCount(); i++ {
			c := d.NamedChild(i)
			if c == nil {
				continue
			}
			if c.Kind() == "function_definition" || c.Kind() == "class_definition" {
				def = c
				break
			}
		}
	}
	if def == nil {
		return
	}
	switch def.Kind() {
	case "function_definition":
		fn := pyExtractFunction(def, src, module, classCtx)
		if fn.Name != "" {
			pf.Functions = append(pf.Functions, fn)
		}
	case "class_definition":
		pyHandleClass(def, src, module, classCtx, pf)
	}
}

// pyExtractFunction populates a FunctionDecl from a function_definition
// node. Receiver is set to the enclosing class context (empty for
// module-level functions).
func pyExtractFunction(fn *tree_sitter.Node, src []byte, module, classCtx string) FunctionDecl {
	name := pyNamedField(fn, "name", src)
	sig := pyBuildSignature(fn, src)
	body := []byte{}
	if b := fn.ChildByFieldName("body"); b != nil {
		body = []byte(b.Utf8Text(src))
	}
	qn := pyQualifiedName(module, classCtx, name)
	startPos := fn.StartPosition()
	endPos := fn.EndPosition()
	return FunctionDecl{
		QualifiedName: qn,
		Receiver:      classCtx,
		Name:          name,
		Signature:     sig,
		StartByte:     uint32(fn.StartByte()),              //nolint:gosec
		EndByte:       uint32(fn.EndByte()),                //nolint:gosec
		StartLine:     uint32(startPos.Row+1) & 0xffffffff, //nolint:gosec
		EndLine:       uint32(endPos.Row+1) & 0xffffffff,   //nolint:gosec
		BodyHash:      hashBytes(body),
	}
}

// pyBuildSignature renders parameters + return type for a function_definition.
func pyBuildSignature(n *tree_sitter.Node, src []byte) string {
	var b strings.Builder
	if params := n.ChildByFieldName("parameters"); params != nil {
		b.WriteString(params.Utf8Text(src))
	}
	if rt := n.ChildByFieldName("return_type"); rt != nil {
		b.WriteString(" -> ")
		b.WriteString(rt.Utf8Text(src))
	}
	return b.String()
}

func pyNamedField(n *tree_sitter.Node, field string, src []byte) string {
	c := n.ChildByFieldName(field)
	if c == nil {
		return ""
	}
	return c.Utf8Text(src)
}

// pyModuleName derives a Python-style dotted module identifier from
// the file path. Per PEP 328 the canonical module path is the package
// hierarchy joined with dots; we approximate using the workspace-
// relative path with separators replaced and the .py extension
// stripped. SCIP-Python uses the same convention.
func pyModuleName(path string) string {
	rel := filepath.ToSlash(path)
	rel = strings.TrimSuffix(rel, ".py")
	rel = strings.TrimSuffix(rel, "/__init__")
	rel = strings.TrimPrefix(rel, "./")
	return strings.ReplaceAll(rel, "/", ".")
}

func pyQualifiedName(module, classCtx, name string) string {
	parts := []string{}
	if module != "" {
		parts = append(parts, module)
	}
	if classCtx != "" {
		parts = append(parts, classCtx)
	}
	if name != "" {
		parts = append(parts, name)
	}
	return strings.Join(parts, ".")
}
