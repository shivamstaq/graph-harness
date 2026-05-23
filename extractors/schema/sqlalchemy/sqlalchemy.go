// Package sqlalchemy implements the SQLAlchemy declarative-models
// schema extractor (Python only). It detects classes that inherit from
// a `Base` (or `DeclarativeBase`) and emits one Schema per `__tablename__`
// declaration + one SchemaField per `Column(...)` assignment.
//
// Both classical SQLAlchemy and SQLAlchemy 2.0 typed `Mapped[...]`
// patterns are supported via two recognisers.
//
// Limits (documented in extractors/schema/README.md):
//   - imperative table mappings (`Table("t", metadata, ...)`) are not
//     parsed.
//   - relationship() columns are skipped.
//   - the Base class is identified by name only — projects that rename
//     it (e.g. `MyBase`) need to mention it in a Pass-2 config override.
package sqlalchemy

import (
	"context"
	"regexp"
	"strings"

	"github.com/shivamstaq/graph-harness/extractors/schema/common"
	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Name is the registry identifier.
const Name = "schema.sqlalchemy"

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
		Languages:  []string{"python"},
		Frameworks: []string{"sqlalchemy"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
	}
}

func accept(path string) bool { return common.HasExt(path, ".py") }

// OnEvent reads the Python file, finds ORM model classes, and emits
// one Schema + N SchemaField events per detected model.
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

// Parse is the test entrypoint.
func Parse(path string, src []byte) []kernel.Event {
	models := findModels(string(src))
	if len(models) == 0 {
		return nil
	}
	producedBy := common.ProducedBy(Name)
	var out []kernel.Event
	for _, m := range models {
		schemaAnchor := common.LangAnchor(m.className, "python", path)
		schema, schemaEv := common.EmitSchema(producedBy, m.tableName, "sqlalchemy", schemaAnchor)
		out = append(out, schemaEv)
		for _, f := range m.fields {
			fieldAnchor := common.QualifiedAnchor(m.tableName+"."+f.name, path)
			_, fieldEv := common.EmitField(producedBy, schema, f.name, f.dataType, f.nullable, fieldAnchor)
			out = append(out, fieldEv)
		}
	}
	return out
}

// --- Parser internals ------------------------------------------------------

type pyModel struct {
	className string
	tableName string
	fields    []pyField
}

type pyField struct {
	name     string
	dataType string
	nullable bool
}

// classRE matches `class Foo(Base):` / `class Foo(DeclarativeBase):` /
// `class Foo(Base, Mixin):` — captures the class name. We look at the
// class body for the model-defining markers below.
var classRE = regexp.MustCompile(`(?m)^class\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(([^)]*)\)\s*:`)

// tablenameRE matches `__tablename__ = "name"` (single or double
// quotes).
var tablenameRE = regexp.MustCompile(`(?m)^\s*__tablename__\s*=\s*["']([^"']+)["']`)

// columnClassicHeadRE locates the start of `name = Column(`. The
// argument list is captured separately via paren-balance scanning so
// nested calls like `Column(String(255), nullable=False)` are handled
// correctly.
var columnClassicHeadRE = regexp.MustCompile(`(?m)^\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*=\s*Column\s*\(`)

// columnMappedHeadRE locates `name: Mapped[...] = mapped_column(`.
// The Mapped[...] generic argument can contain nested brackets (e.g.
// `Mapped[Optional[str]]`), so we use `.+?` and rely on the surrounding
// `= mapped_column(` anchor to keep the match tight.
var columnMappedHeadRE = regexp.MustCompile(`(?m)^\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*:\s*Mapped\[(.+?)\]\s*=\s*mapped_column\s*\(`)

// relationshipRE matches `name = relationship(...)` lines — these
// must be skipped because they're not columns.
var relationshipRE = regexp.MustCompile(`(?m)^\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*=\s*relationship\s*\(`)

// findModels walks the file's classes. A class is treated as a model
// when its base list includes "Base" or "DeclarativeBase" (literal
// name) AND it declares a `__tablename__`. Both signals are required
// to avoid emitting Schema rows for mixin or pure-data classes.
func findModels(src string) []pyModel {
	heads := classRE.FindAllStringSubmatchIndex(src, -1)
	var out []pyModel
	for i, h := range heads {
		bases := src[h[4]:h[5]]
		if !isModelBase(bases) {
			continue
		}
		// Body extends from the line after the colon to the start of
		// the next class header at column 0 (or end of file).
		bodyStart := h[1]
		bodyEnd := len(src)
		if i+1 < len(heads) {
			bodyEnd = heads[i+1][0]
		}
		body := src[bodyStart:bodyEnd]
		tname := tablenameRE.FindStringSubmatch(body)
		if tname == nil {
			continue
		}
		out = append(out, pyModel{
			className: src[h[2]:h[3]],
			tableName: tname[1],
			fields:    parsePyFields(body),
		})
	}
	return out
}

// isModelBase returns true when the parenthesised base list looks
// like a declarative-mapping anchor. We accept literal `Base`, the
// 2.0 `DeclarativeBase` class, and the `db.Model` Flask-SQLAlchemy
// convention.
func isModelBase(bases string) bool {
	parts := strings.Split(bases, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch p {
		case "Base", "DeclarativeBase", "db.Model":
			return true
		}
	}
	return false
}

// parsePyFields finds the Column / mapped_column declarations in the
// class body. relationship() fields are skipped.
func parsePyFields(body string) []pyField {
	var out []pyField
	skip := map[string]bool{}
	for _, m := range relationshipRE.FindAllStringSubmatch(body, -1) {
		skip[m[1]] = true
	}
	// Classical syntax: `name = Column(...)`.
	for _, m := range columnClassicHeadRE.FindAllStringSubmatchIndex(body, -1) {
		// m[1] points at the byte AFTER the opening `(`, so paren
		// scanning starts at the `(` one before.
		args, ok := readParenArgs(body, m[1]-1)
		if !ok {
			continue
		}
		name := body[m[2]:m[3]]
		if skip[name] {
			continue
		}
		out = append(out, pyField{
			name:     name,
			dataType: extractColumnType(args),
			nullable: detectNullable(args),
		})
	}
	// SQLAlchemy 2.0 typed mapped_column syntax.
	for _, m := range columnMappedHeadRE.FindAllStringSubmatchIndex(body, -1) {
		args, ok := readParenArgs(body, m[1]-1)
		if !ok {
			continue
		}
		name := body[m[2]:m[3]]
		mappedT := body[m[4]:m[5]]
		if skip[name] {
			continue
		}
		out = append(out, pyField{
			name:     name,
			dataType: strings.TrimSpace(mappedT),
			nullable: detectNullable(args) || strings.Contains(mappedT, "Optional"),
		})
	}
	return out
}

// readParenArgs returns the substring inside the parens that open at
// openIdx, respecting nested parens. The opening `(` MUST live at
// src[openIdx]. Returns (args, true) when a matching `)` is found.
func readParenArgs(src string, openIdx int) (string, bool) {
	if openIdx < 0 || openIdx >= len(src) || src[openIdx] != '(' {
		return "", false
	}
	depth := 1
	for i := openIdx + 1; i < len(src); i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return src[openIdx+1 : i], true
			}
		case '"', '\'':
			q := src[i]
			i++
			for i < len(src) && src[i] != q {
				if src[i] == '\\' && i+1 < len(src) {
					i++
				}
				i++
			}
		}
	}
	return "", false
}

// extractColumnType picks the first positional argument of a
// Column(...) call. Common Types like `Integer`, `String(255)`,
// `DateTime` start the args list before any keyword arguments.
func extractColumnType(args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return "unknown"
	}
	first := args
	// Stop at first top-level comma.
	depth := 0
	for i := 0; i < len(args); i++ {
		c := args[i]
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				first = args[:i]
				goto done
			}
		}
	}
done:
	first = strings.TrimSpace(first)
	// Drop trailing `(...)` for parameterised types.
	if idx := strings.Index(first, "("); idx > 0 {
		first = first[:idx]
	}
	return first
}

// detectNullable returns true when the args list explicitly says so.
// SQLAlchemy's default is nullable=True for non-primary-key columns,
// which we honour.
func detectNullable(args string) bool {
	if strings.Contains(args, "nullable=False") {
		return false
	}
	if strings.Contains(args, "primary_key=True") {
		return false
	}
	if strings.Contains(args, "nullable=True") {
		return true
	}
	return true
}

func init() {
	cf.Register(Name, New, cf.Descriptor{
		Family:     "schemas",
		Languages:  []string{"python"},
		Frameworks: []string{"sqlalchemy"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
		Inputs:     []cf.EventKind{cf.InputCoreFileChanged},
		Outputs:    []cf.EntityKind{cf.KindSchema, cf.KindSchemaField},
	})
}
