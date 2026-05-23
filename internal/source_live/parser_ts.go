package source_live

import (
	"fmt"
	"path/filepath"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// ParseTypeScriptFile runs the tree-sitter TypeScript / TSX grammar
// over src and returns a ParsedFile. Language id is "typescript" for
// .ts/.tsx/.js/.jsx files (we treat the JS family as TypeScript with
// implicit any so the grammar covers both — JSX-specific syntax is
// handled by the TSX grammar variant for .tsx/.jsx).
//
// Tolerant of parse errors per SPEC §6.11 (medium confidence,
// error-tolerant): functions before the first syntax error are still
// emitted, and an empty file produces an empty Functions list with
// no error.
func ParseTypeScriptFile(path string, src []byte) (*ParsedFile, error) {
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
	defer tree.Close()
	root := tree.RootNode()

	pf := &ParsedFile{
		Path:     path,
		Language: "typescript",
		BodyHash: hashBytes(src),
	}

	moduleName := tsModuleName(path)
	tsCollectFunctions(root, src, moduleName, "", pf)
	return pf, nil
}

// tsCollectFunctions walks the tree, gathering top-level functions and
// class methods. nameSpace is the dotted prefix accumulated through
// nested namespaces / classes (e.g. "module.MyClass").
func tsCollectFunctions(n *tree_sitter.Node, src []byte, module, classCtx string, pf *ParsedFile) {
	if n == nil {
		return
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "function_declaration", "generator_function_declaration":
			fn := tsExtractFunction(c, src, module, classCtx, "")
			if fn.Name != "" {
				pf.Functions = append(pf.Functions, fn)
			}
		case "class_declaration", "abstract_class_declaration":
			name := tsNamedField(c, "name", src)
			qn := tsQualifiedName(module, classCtx, name)
			pf.TypeDecls = append(pf.TypeDecls, tsExtractTypeDecl(c, src, qn, name, TypeDeclKindClass))
			body := c.ChildByFieldName("body")
			if body != nil {
				tsCollectClassMembers(body, src, module, joinDotted(classCtx, name), pf)
			}
		case "interface_declaration":
			name := tsNamedField(c, "name", src)
			qn := tsQualifiedName(module, classCtx, name)
			pf.TypeDecls = append(pf.TypeDecls, tsExtractTypeDecl(c, src, qn, name, TypeDeclKindInterface))
			// Interface method signatures have no executable bodies; the
			// FunctionDecl pipeline intentionally skips them. We still
			// emit the interface itself as a TypeDecl so the selector
			// resolver and code.core can address it.
		case "internal_module", "module", "namespace_declaration":
			name := tsNamedField(c, "name", src)
			body := c.ChildByFieldName("body")
			if body != nil {
				tsCollectFunctions(body, src, joinDotted(module, name), classCtx, pf)
			}
		case "lexical_declaration", "variable_declaration":
			tsCollectArrowsInDeclarations(c, src, module, classCtx, pf)
		case "export_statement":
			// Recurse into the body of `export function ...` /
			// `export class ...`.
			tsCollectFunctions(c, src, module, classCtx, pf)
		case "program":
			tsCollectFunctions(c, src, module, classCtx, pf)
		}
	}
}

// tsCollectClassMembers walks a class_body, emitting MethodDeclarations
// for each method_definition / public_field_definition with an arrow
// function value.
func tsCollectClassMembers(body *tree_sitter.Node, src []byte, module, classCtx string, pf *ParsedFile) {
	for i := uint(0); i < body.NamedChildCount(); i++ {
		c := body.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "method_definition", "abstract_method_signature":
			fn := tsExtractFunction(c, src, module, classCtx, classCtx)
			if fn.Name != "" {
				pf.Functions = append(pf.Functions, fn)
			}
		case "public_field_definition", "field_definition":
			value := c.ChildByFieldName("value")
			if value == nil {
				continue
			}
			if value.Kind() == "arrow_function" || value.Kind() == "function_expression" {
				name := tsNamedField(c, "name", src)
				fn := tsExtractArrowField(value, src, module, classCtx, name)
				if fn.Name != "" {
					pf.Functions = append(pf.Functions, fn)
				}
			}
		}
	}
}

// tsCollectArrowsInDeclarations finds `const foo = () => { ... }` and
// `const foo = function () { ... }` forms at module scope.
func tsCollectArrowsInDeclarations(decl *tree_sitter.Node, src []byte, module, classCtx string, pf *ParsedFile) {
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		v := decl.NamedChild(i)
		if v == nil || v.Kind() != "variable_declarator" {
			continue
		}
		name := tsNamedField(v, "name", src)
		value := v.ChildByFieldName("value")
		if value == nil || name == "" {
			continue
		}
		if value.Kind() == "arrow_function" || value.Kind() == "function_expression" {
			fn := tsExtractArrowField(value, src, module, classCtx, name)
			if fn.Name != "" {
				pf.Functions = append(pf.Functions, fn)
			}
		}
	}
}

// tsExtractFunction populates a FunctionDecl from a function_declaration
// or method_definition node.
func tsExtractFunction(n *tree_sitter.Node, src []byte, module, classCtx, recv string) FunctionDecl {
	name := tsNamedField(n, "name", src)
	sig := tsBuildSignature(n, src)
	body := []byte{}
	if b := n.ChildByFieldName("body"); b != nil {
		body = []byte(b.Utf8Text(src))
	}
	qn := tsQualifiedName(module, classCtx, name)
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

// tsExtractArrowField builds a FunctionDecl for `const foo = () => ...`
// or `class { foo = () => ... }`. The bound name comes from the
// surrounding declarator/field, not from the arrow itself.
func tsExtractArrowField(arrow *tree_sitter.Node, src []byte, module, classCtx, name string) FunctionDecl {
	sig := tsBuildSignature(arrow, src)
	body := []byte{}
	if b := arrow.ChildByFieldName("body"); b != nil {
		body = []byte(b.Utf8Text(src))
	}
	recv := classCtx
	qn := tsQualifiedName(module, classCtx, name)
	startPos := arrow.StartPosition()
	endPos := arrow.EndPosition()
	return FunctionDecl{
		QualifiedName: qn,
		Receiver:      recv,
		Name:          name,
		Signature:     sig,
		StartByte:     uint32(arrow.StartByte()),           //nolint:gosec
		EndByte:       uint32(arrow.EndByte()),             //nolint:gosec
		StartLine:     uint32(startPos.Row+1) & 0xffffffff, //nolint:gosec
		EndLine:       uint32(endPos.Row+1) & 0xffffffff,   //nolint:gosec
		BodyHash:      hashBytes(body),
	}
}

// tsExtractTypeDecl builds a TypeDeclDecl for a class_declaration /
// abstract_class_declaration / interface_declaration node. BodyHash is
// the sha256 of the declaration's full source span — there is no
// per-class "body bytes" distinct from its span the way functions
// have, but a span hash still satisfies the body_hash anchor's
// "identical text → identical id" property.
func tsExtractTypeDecl(n *tree_sitter.Node, src []byte, qn, name string, kind TypeDeclKind) TypeDeclDecl {
	startPos := n.StartPosition()
	endPos := n.EndPosition()
	bytes := n.Utf8Text(src)
	return TypeDeclDecl{
		QualifiedName: qn,
		Name:          name,
		Kind:          kind,
		StartByte:     uint32(n.StartByte()),               //nolint:gosec
		EndByte:       uint32(n.EndByte()),                 //nolint:gosec
		StartLine:     uint32(startPos.Row+1) & 0xffffffff, //nolint:gosec
		EndLine:       uint32(endPos.Row+1) & 0xffffffff,   //nolint:gosec
		BodyHash:      hashBytes([]byte(bytes)),
	}
}

// tsBuildSignature renders parameters + return type for a function-like
// node. Falls back to an empty string if neither field is present.
func tsBuildSignature(n *tree_sitter.Node, src []byte) string {
	var b strings.Builder
	if params := n.ChildByFieldName("parameters"); params != nil {
		b.WriteString(params.Utf8Text(src))
	}
	if rt := n.ChildByFieldName("return_type"); rt != nil {
		b.WriteString(" ")
		b.WriteString(rt.Utf8Text(src))
	}
	return b.String()
}

func tsNamedField(n *tree_sitter.Node, field string, src []byte) string {
	c := n.ChildByFieldName(field)
	if c == nil {
		return ""
	}
	return c.Utf8Text(src)
}

// tsModuleName derives a TypeScript module identifier from the file
// path: workspace-relative, slash-to-dot, extension stripped. Mirrors
// the convention SCIP indexers and ts-language-server use when no
// package.json scope is in play.
func tsModuleName(path string) string {
	base := filepath.Base(path)
	for _, ext := range []string{".tsx", ".ts", ".jsx", ".js"} {
		if strings.HasSuffix(strings.ToLower(base), ext) {
			base = base[:len(base)-len(ext)]
			break
		}
	}
	return base
}

// tsQualifiedName composes the dotted qualified name for a TypeScript
// symbol. classCtx is empty for top-level functions and "ClassName"
// (or "Outer.Inner") for class members.
func tsQualifiedName(module, classCtx, name string) string {
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

func joinDotted(prefix, name string) string {
	if prefix == "" {
		return name
	}
	if name == "" {
		return prefix
	}
	return prefix + "." + name
}
