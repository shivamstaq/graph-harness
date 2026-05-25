// Package gorm implements the GORM schema extractor (Go only). It
// detects struct types that look like GORM models — fields with
// backtick-quoted `gorm:"..."` tags or struct types that embed
// `gorm.Model` — and emits one Schema + one SchemaField per scalar
// field.
//
// Table-name detection prefers an explicit `TableName() string`
// receiver method declared in the same file; otherwise the snake_case
// lowercased plural of the struct name is used (GORM's
// `schema.NamingStrategy` default). The fallback follows GORM's
// behaviour but, because we don't run go/types, it can miss
// project-level overrides; this is documented in extractors/schema/README.md.
//
// Field anchors use language_id="go" + qualified_name="<pkg>.<Struct>.<Field>"
// where <pkg> is inferred from the package clause in the same file.
package gorm

import (
	"context"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"unicode"

	"go/ast"

	"github.com/shivamstaq/graph-harness/extractors/schema/common"
	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Name is the registry identifier.
const Name = "schema.gorm"

type extractor struct {
	workspace string
	logf      func(string, ...any)
}

// New constructs an extractor instance.
func New(deps cf.Deps) (cf.Extractor, error) {
	return &extractor{workspace: deps.Workspace, logf: deps.Logf}, nil
}

func (e *extractor) Name() string                  { return Name }
func (e *extractor) Inputs() []cf.EventKind        { return []cf.EventKind{cf.InputCoreFileChanged} }
func (e *extractor) Outputs() []cf.EntityKind {
	return []cf.EntityKind{cf.KindSchema, cf.KindSchemaField}
}
func (e *extractor) Capabilities() cf.Capabilities {
	return cf.Capabilities{
		Family:     "schemas",
		Languages:  []string{"go"},
		Frameworks: []string{"gorm"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
	}
}

func accept(path string) bool { return common.HasExt(path, ".go") }

// OnEvent reads the changed Go file and emits Schema + SchemaField
// events for every GORM-model-looking struct declared in it.
func (e *extractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	_ = ctx
	path, ok := common.NoopOnPathMiss(ctx, in, accept)
	if !ok {
		return nil, nil
	}
	src, err := common.ReadFile(e.workspace, path)
	if err != nil {
		return nil, nil
	}
	return Parse(path, src), nil
}

// Parse is the test entrypoint. We use go/parser for robustness — the
// struct-tag and method-receiver shapes are exactly what go/ast
// surfaces, so a regex would just reinvent it.
func Parse(path string, src []byte) []kernel.Event {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		// Try the raw-regex fallback so syntax errors don't black
		// out the whole file (suppress-at-source semantics).
		return parseRegexFallback(path, string(src))
	}
	pkg := f.Name.Name
	tableNames := collectTableNames(f) // struct -> table

	producedBy := common.ProducedBy(Name)
	var out []kernel.Event
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			fields := collectFields(st)
			if !isGormModel(st, fields) {
				continue
			}
			table := tableNames[ts.Name.Name]
			if table == "" {
				table = defaultTableName(ts.Name.Name)
			}
			qn := pkg + "." + ts.Name.Name
			schemaAnchor := common.LangAnchor(qn, "go", path)
			schema, schemaEv := common.EmitSchema(producedBy, table, "gorm", schemaAnchor)
			out = append(out, schemaEv)
			for _, gf := range fields {
				if gf.skip {
					continue
				}
				fieldAnchor := common.LangAnchor(qn+"."+gf.name, "go", path)
				_, fieldEv := common.EmitField(producedBy, schema, gf.colName, gf.dataType, gf.nullable, fieldAnchor)
				out = append(out, fieldEv)
			}
		}
	}
	return out
}

// --- Parser internals ------------------------------------------------------

type gormField struct {
	name     string // Go field name
	colName  string // database column name
	dataType string // Go type as rendered
	nullable bool
	skip     bool // explicitly `gorm:"-"`
}

// collectFields walks the struct fields, parses any `gorm:"..."` tag
// and produces one gormField per non-skipped column. Embedded fields
// (no name) other than `gorm.Model` are ignored.
func collectFields(st *ast.StructType) []gormField {
	var out []gormField
	for _, fld := range st.Fields.List {
		tag := ""
		if fld.Tag != nil {
			tag = strings.Trim(fld.Tag.Value, "`")
		}
		gormTag := extractTagValue(tag, "gorm")
		// Detect embedded `gorm.Model` — expand into its 4 fields
		// (ID, CreatedAt, UpdatedAt, DeletedAt) so callers see the
		// columns even without a tag.
		if len(fld.Names) == 0 {
			if sel, ok := fld.Type.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "gorm" && sel.Sel.Name == "Model" {
					out = append(out,
						gormField{name: "ID", colName: "id", dataType: "uint", nullable: false},
						gormField{name: "CreatedAt", colName: "created_at", dataType: "time.Time", nullable: false},
						gormField{name: "UpdatedAt", colName: "updated_at", dataType: "time.Time", nullable: false},
						gormField{name: "DeletedAt", colName: "deleted_at", dataType: "gorm.DeletedAt", nullable: true},
					)
					continue
				}
			}
			continue
		}
		dataType := exprString(fld.Type)
		col := tagPart(gormTag, "column")
		notnull := strings.Contains(gormTag, "not null")
		primaryKey := strings.Contains(gormTag, "primaryKey") || strings.Contains(gormTag, "primary_key")
		skip := gormTag == "-"
		for _, n := range fld.Names {
			colName := col
			if colName == "" {
				colName = snakeCase(n.Name)
			}
			out = append(out, gormField{
				name:     n.Name,
				colName:  colName,
				dataType: dataType,
				nullable: !notnull && !primaryKey && isPointer(fld.Type),
				skip:     skip,
			})
		}
	}
	return out
}

// isGormModel returns true when the struct has at least one field
// with a `gorm:"..."` tag OR embeds `gorm.Model`. The all-tags-empty
// case (a plain struct that happens to live next to GORM code) is
// rejected.
func isGormModel(st *ast.StructType, fields []gormField) bool {
	for _, fld := range st.Fields.List {
		if fld.Tag != nil && strings.Contains(fld.Tag.Value, `gorm:`) {
			return true
		}
		if len(fld.Names) == 0 {
			if sel, ok := fld.Type.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "gorm" && sel.Sel.Name == "Model" {
					return true
				}
			}
		}
	}
	_ = fields
	return false
}

// collectTableNames walks the file's func decls for `func (Foo) TableName()
// string { return "..." }` and returns a struct->table mapping.
func collectTableNames(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		if fn.Name.Name != "TableName" {
			continue
		}
		recvType := exprString(fn.Recv.List[0].Type)
		recvType = strings.TrimPrefix(recvType, "*")
		// Body must be `return "..."`.
		if fn.Body == nil {
			continue
		}
		for _, stmt := range fn.Body.List {
			ret, ok := stmt.(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				continue
			}
			if lit, ok := ret.Results[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				out[recvType] = strings.Trim(lit.Value, "\"`")
				break
			}
		}
	}
	return out
}

// extractTagValue returns the value of the given key in a struct-tag
// string (`gorm:"primaryKey" json:"id"`).
func extractTagValue(tag, key string) string {
	for tag != "" {
		// Skip leading space.
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag = tag[i:]
		if tag == "" {
			break
		}
		// Find ':'.
		i = 0
		for i < len(tag) && tag[i] != ':' {
			i++
		}
		if i >= len(tag) {
			break
		}
		k := tag[:i]
		tag = tag[i+1:]
		if len(tag) == 0 || tag[0] != '"' {
			break
		}
		// Find closing quote.
		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' && i+1 < len(tag) {
				i += 2
				continue
			}
			i++
		}
		if i >= len(tag) {
			break
		}
		v := tag[1:i]
		if k == key {
			return v
		}
		tag = tag[i+1:]
	}
	return ""
}

// tagPart returns the value of the `key:value` part in a GORM tag
// such as `column:foo;not null;index`.
func tagPart(tag, key string) string {
	for _, part := range strings.Split(tag, ";") {
		p := strings.TrimSpace(part)
		if strings.HasPrefix(p, key+":") {
			return strings.TrimSpace(p[len(key)+1:])
		}
	}
	return ""
}

// exprString renders a type expression. Best-effort for the common
// shapes (Ident, StarExpr, ArrayType, SelectorExpr).
func exprString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		return "[]" + exprString(t.Elt)
	case *ast.MapType:
		return "map[" + exprString(t.Key) + "]" + exprString(t.Value)
	default:
		return "unknown"
	}
}

// isPointer returns true if the type is a *Foo expression.
func isPointer(e ast.Expr) bool {
	_, ok := e.(*ast.StarExpr)
	return ok
}

// defaultTableName returns the GORM default of snake_case pluralised
// struct name. We do a *very* dumb pluralisation: append "s" unless
// the name already ends with "s", or matches a couple of common
// irregulars.
func defaultTableName(name string) string {
	s := snakeCase(name)
	switch {
	case strings.HasSuffix(s, "s"):
		return s
	case strings.HasSuffix(s, "y"):
		return s[:len(s)-1] + "ies"
	default:
		return s + "s"
	}
}

// snakeCase converts CamelCase to snake_case, handling acronym runs
// the way GORM's NamingStrategy does: an underscore is inserted before
// an uppercase rune only at a real word boundary —
//
//   - lower/digit → upper  (userID    → user_id, the I)
//   - upper → upper-then-lower (HTTPServer → http_server, the S)
//
// so consecutive-uppercase initialisms stay together:
// ID → id, UserID → user_id, HTTPServer → http_server. The previous
// naive "underscore before every uppercase" produced i_d / user_i_d /
// h_t_t_p_server.
func snakeCase(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		if i > 0 && unicode.IsUpper(r) {
			prevUpper := unicode.IsUpper(rs[i-1])
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if !prevUpper || nextLower {
				b.WriteByte('_')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// parseRegexFallback is the malformed-source path. We do not attempt
// to recover everything go/parser would; just look for `type Foo
// struct {` blocks with at least one `gorm:` tag. This keeps the
// suppress-at-source semantics: if a file parses cleanly later, the
// AST path emits the proper events.
var fallbackHeaderRE = regexp.MustCompile(`(?m)^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\s+struct\s*\{`)

func parseRegexFallback(path, src string) []kernel.Event {
	if !strings.Contains(src, `gorm:`) && !strings.Contains(src, `gorm.Model`) {
		return nil
	}
	matches := fallbackHeaderRE.FindAllStringSubmatch(src, -1)
	if len(matches) == 0 {
		return nil
	}
	producedBy := common.ProducedBy(Name)
	pkg := ""
	if i := strings.Index(src, "package "); i >= 0 {
		rest := src[i+len("package "):]
		if j := strings.IndexAny(rest, " \n\r\t;"); j > 0 {
			pkg = rest[:j]
		}
	}
	var out []kernel.Event
	for _, m := range matches {
		qn := pkg + "." + m[1]
		anchor := common.LangAnchor(qn, "go", path)
		_, ev := common.EmitSchema(producedBy, defaultTableName(m[1]), "gorm", anchor)
		out = append(out, ev)
	}
	return out
}

func init() {
	cf.Register(Name, New, cf.Descriptor{
		Family:     "schemas",
		Languages:  []string{"go"},
		Frameworks: []string{"gorm"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
		Inputs:     []cf.EventKind{cf.InputCoreFileChanged},
		Outputs:    []cf.EntityKind{cf.KindSchema, cf.KindSchemaField},
	})
}
