// Package drizzle implements the Drizzle ORM schema extractor (TS
// only). It detects calls to `pgTable("name", {...})`, `mysqlTable`,
// and `sqliteTable` and emits one Schema per call + one SchemaField
// per column declared in the column object.
//
// Recognition is regex-based, not full-TS-AST: Drizzle's schema files
// follow a stylized shape and a tree-sitter pass over them would not
// add much precision for the v1 surface. Limits (which are documented
// in extractors/schema/README.md):
//   - dynamic table names (`pgTable(TABLE_NAME, ...)`) are skipped.
//   - column types are detected by the helper function name
//     (`integer`, `text`, `varchar`, `timestamp`, etc.).
//   - `.notNull()` / `.primaryKey()` are read out of the per-column
//     suffix to set the Nullable flag.
package drizzle

import (
	"context"
	"regexp"
	"strings"

	"github.com/shivamstaq/graph-harness/extractors/schema/common"
	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Name is the registry identifier.
const Name = "schema.drizzle"

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
		Languages:  []string{"typescript"},
		Frameworks: []string{"drizzle"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
	}
}

func accept(path string) bool {
	return common.HasExt(path, ".ts", ".tsx", ".mts", ".cts", ".js", ".mjs")
}

// OnEvent reads the changed file and parses any drizzle table
// declarations it contains. Files with no drizzle imports/usage
// produce no events — the dispatcher records this as suppress-at-
// source.
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

// Parse is the test entrypoint: given a file's source, return the
// events that would be emitted by OnEvent.
func Parse(path string, src []byte) []kernel.Event {
	tables := findTables(string(src))
	if len(tables) == 0 {
		return nil
	}
	producedBy := common.ProducedBy(Name)
	var out []kernel.Event
	for _, t := range tables {
		// Anchor on the JS/TS export identifier when available — that
		// is the cross-language qualified_name the rest of the system
		// will see. Fall back to the table string if no identifier
		// could be inferred.
		anchorName := t.exportName
		if anchorName == "" {
			anchorName = t.table
		}
		schemaAnchor := common.LangAnchor(anchorName, "typescript", path)
		schema, schemaEv := common.EmitSchema(producedBy, t.table, "drizzle", schemaAnchor)
		out = append(out, schemaEv)
		for _, f := range t.fields {
			fieldAnchor := common.QualifiedAnchor(t.table+"."+f.name, path)
			_, fieldEv := common.EmitField(producedBy, schema, f.name, f.dataType, f.nullable, fieldAnchor)
			out = append(out, fieldEv)
		}
	}
	return out
}

// --- Parser internals ------------------------------------------------------

type drizzleTable struct {
	exportName string // optional — `export const users = pgTable(...)`
	table      string // the literal table name passed as the 1st arg
	fields     []drizzleField
}

type drizzleField struct {
	name     string
	dataType string
	nullable bool
}

// tableCallRE matches `pgTable("foos", { ... })` and the mysql/sqlite
// variants. The submatch groups are:
//   1: function name (pgTable/mysqlTable/sqliteTable)
//   2: table name (string literal)
// We then locate the matching `{...}` separately because regex
// alternation can't balance braces.
var tableCallRE = regexp.MustCompile(`(pg|mysql|sqlite)Table\(\s*["']([^"']+)["']\s*,\s*\{`)

// exportRE captures the export const identifier on the line that
// contains the table call: `export const users = pgTable(...)`.
var exportRE = regexp.MustCompile(`(?m)(?:export\s+)?(?:const|let|var)\s+([a-zA-Z_][a-zA-Z0-9_]*)\s*=\s*$`)

// columnRE captures one column declaration:
//   `name: text("name").notNull().primaryKey()` or
//   `id: serial("id").primaryKey()`
// We pick:
//   1: property name (column identifier in TS object)
//   2: column type function (text/integer/varchar/...)
//   3: tail (everything after `("col_name")` up to the next comma at
//      brace-depth 1) — used to detect `.notNull()`.
var columnRE = regexp.MustCompile(`(?m)^\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*:\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\(`)

// findTables walks the source for table-call sites and parses the
// column object for each.
func findTables(src string) []drizzleTable {
	clean := stripBlockAndLineComments(src)
	var out []drizzleTable
	for _, m := range tableCallRE.FindAllStringSubmatchIndex(clean, -1) {
		// m[0] = start of `pgTable("foos", {` ; the matched `{` is at
		// m[1]-1. Find the matching `}` by depth-count.
		braceOpen := m[1] - 1
		braceClose := findMatchingBrace(clean, braceOpen)
		if braceClose < 0 {
			continue
		}
		body := clean[braceOpen+1 : braceClose]
		t := drizzleTable{
			table:  clean[m[4]:m[5]],
			fields: parseColumns(body),
		}
		// Try to find the export const above the call site.
		if before := clean[:m[0]]; before != "" {
			// Trim trailing whitespace; look at the last 200 chars
			// to keep the regex fast.
			start := 0
			if len(before) > 200 {
				start = len(before) - 200
			}
			window := strings.TrimRight(before[start:], " \t")
			if em := exportRE.FindStringSubmatch(window); em != nil {
				t.exportName = em[1]
			}
		}
		out = append(out, t)
	}
	return out
}

// parseColumns extracts column declarations from the object body. We
// scan line-by-line for `<name>: <type>(...)` patterns and inspect
// the tail of the line for `.notNull()`.
func parseColumns(body string) []drizzleField {
	var out []drizzleField
	for _, line := range strings.Split(body, "\n") {
		m := columnRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		dataType := m[2]
		// Skip obvious non-column helpers in case they appear inline.
		if dataType == "primaryKey" || dataType == "index" || dataType == "uniqueIndex" || dataType == "foreignKey" {
			continue
		}
		nullable := !strings.Contains(line, ".notNull(") && !strings.Contains(line, ".primaryKey(")
		out = append(out, drizzleField{name: name, dataType: dataType, nullable: nullable})
	}
	return out
}

// findMatchingBrace returns the offset of the `}` that closes the `{`
// at openIdx. Returns -1 if there's no balanced pair. Strings are
// honoured naively (no escape handling) which is enough for Drizzle
// schemas.
func findMatchingBrace(src string, openIdx int) int {
	depth := 1
	i := openIdx + 1
	for i < len(src) {
		switch src[i] {
		case '"', '\'', '`':
			// Skip to closing quote.
			q := src[i]
			i++
			for i < len(src) && src[i] != q {
				if src[i] == '\\' && i+1 < len(src) {
					i += 2
					continue
				}
				i++
			}
			i++
		case '{':
			depth++
			i++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
			i++
		default:
			i++
		}
	}
	return -1
}

// stripBlockAndLineComments removes `/* ... */` and `//...` comments
// so the table-recognising regex never lands inside one.
func stripBlockAndLineComments(src string) string {
	var b strings.Builder
	i := 0
	for i < len(src) {
		if i+1 < len(src) && src[i] == '/' && src[i+1] == '/' {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(src) && src[i] == '/' && src[i+1] == '*' {
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i += 2
			continue
		}
		b.WriteByte(src[i])
		i++
	}
	return b.String()
}

func init() {
	cf.Register(Name, New, cf.Descriptor{
		Family:     "schemas",
		Languages:  []string{"typescript"},
		Frameworks: []string{"drizzle"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
		Inputs:     []cf.EventKind{cf.InputCoreFileChanged},
		Outputs:    []cf.EntityKind{cf.KindSchema, cf.KindSchemaField},
	})
}
