// Package rawsql implements the cross-language raw-SQL schema
// extractor. It scans source files for SQL string literals and
// detects DDL + DML at a best-effort level:
//
//   - CREATE TABLE foo (col TYPE, ...) — emits Schema + SchemaField
//   - INSERT INTO foo (a, b) ...        — emits SchemaWrite per column
//   - UPDATE foo SET a = ?, b = ?       — emits SchemaWrite per column
//   - SELECT a, b FROM foo               — emits SchemaRead per column
//
// Recognition is pure-Go regex + a tiny tokenizer; we do NOT pull in
// sqlglot/wazero/cgo. The plan's `risks` row called for WASM-hosted
// sqlglot but allowed a relaxation; see extractors/schema/README.md
// for the documented v1 limits.
//
// The extractor is language-agnostic — it walks Go, Python, TS, JS, and
// .sql files alike. SQL inside string literals is detected by looking
// for the leading keyword (CREATE TABLE / INSERT INTO / UPDATE / SELECT)
// case-insensitively; literals that don't match are ignored.
package rawsql

import (
	"context"
	"regexp"
	"strings"

	"github.com/shivamstaq/graph-harness/extractors/schema/common"
	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Name is the registry identifier.
const Name = "schema.rawsql"

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
	return []cf.EntityKind{cf.KindSchema, cf.KindSchemaField, cf.KindSchemaRead, cf.KindSchemaWrite}
}
func (e *extractor) Capabilities() cf.Capabilities {
	return cf.Capabilities{
		Family:     "schemas",
		Languages:  []string{"go", "typescript", "python"},
		Frameworks: []string{"raw-sql"},
		Fallback:   cf.FallbackLossy,
		BatchHint:  cf.BatchPerEvent,
	}
}

func accept(path string) bool {
	return common.HasExt(path, ".sql", ".go", ".py", ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".mjs")
}

// OnEvent reads the changed file, extracts SQL statements, and emits
// Schema/SchemaField/SchemaRead/SchemaWrite per detected statement.
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
	stmts := extractStatements(path, string(src))
	if len(stmts) == 0 {
		return nil
	}
	producedBy := common.ProducedBy(Name)
	var out []kernel.Event
	// Track schemas we've already emitted in this pass so per-file
	// re-references collapse to one Schema row.
	seenTable := map[string]cf.Schema{}
	for _, st := range stmts {
		switch st.kind {
		case stmtCreateTable:
			schemaAnchor := common.PathAnchor(path)
			schema, schemaEv := common.EmitSchema(producedBy, st.table, "sql", schemaAnchor)
			seenTable[st.table] = schema
			out = append(out, schemaEv)
			for _, c := range st.columns {
				fieldAnchor := common.QualifiedAnchor(st.table+"."+c.name, path)
				_, fieldEv := common.EmitField(producedBy, schema, c.name, c.dataType, c.nullable, fieldAnchor)
				out = append(out, fieldEv)
			}
		case stmtInsert, stmtUpdate:
			// Best-effort field-id: encode "<table>.<column>" so a
			// later SchemaField with the matching qualified_name can
			// be joined by the consumer; suppress-at-source treats
			// these as forward references rather than hard links.
			anchor := common.PathAnchor(path)
			for _, c := range st.columns {
				fid := st.table + "." + c.name
				out = append(out, common.EmitWrite(producedBy, fid, anchor))
			}
		case stmtSelect:
			anchor := common.PathAnchor(path)
			for _, c := range st.columns {
				fid := st.table + "." + c.name
				out = append(out, common.EmitRead(producedBy, fid, anchor))
			}
		}
	}
	return out
}

// --- Statement extraction --------------------------------------------------

type stmtKind int

const (
	stmtCreateTable stmtKind = iota
	stmtInsert
	stmtUpdate
	stmtSelect
)

type sqlStatement struct {
	kind    stmtKind
	table   string
	columns []sqlColumn
}

type sqlColumn struct {
	name     string
	dataType string
	nullable bool
}

// extractStatements finds SQL inside source. For .sql files we treat
// the whole file as SQL; for source files we scan string literals
// (single, double, backtick, triple-quoted Python strings) and try
// each.
func extractStatements(path, src string) []sqlStatement {
	var literals []string
	if strings.HasSuffix(strings.ToLower(path), ".sql") {
		literals = []string{src}
	} else {
		literals = scanStringLiterals(src)
	}
	var out []sqlStatement
	for _, lit := range literals {
		out = append(out, parseSQL(lit)...)
	}
	return out
}

// scanStringLiterals returns all string-literal payloads in a source
// file. Supports:
//   - "double" and 'single' quotes (Go, Py, TS, JS)
//   - `backtick` strings (Go, JS template literals)
//   - """triple-double""" and '''triple-single''' (Python)
// Escape sequences are honoured by skipping the next char after a
// backslash; this is correct for the unicode-free SQL we care about.
func scanStringLiterals(src string) []string {
	var out []string
	i := 0
	for i < len(src) {
		// Skip // and /* */ comments to avoid matching SQL inside them.
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
		// Skip Python `#` comments at line start (best-effort).
		if src[i] == '#' && (i == 0 || src[i-1] == '\n') {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		}
		// Python triple-quotes — check before single-quote since """ is
		// a superset of ".
		if i+2 < len(src) && (src[i] == '"' || src[i] == '\'') &&
			src[i+1] == src[i] && src[i+2] == src[i] {
			q := src[i]
			i += 3
			start := i
			for i+2 < len(src) && !(src[i] == q && src[i+1] == q && src[i+2] == q) {
				i++
			}
			if i+2 < len(src) {
				out = append(out, src[start:i])
				i += 3
			} else {
				break
			}
			continue
		}
		if src[i] == '"' || src[i] == '\'' || src[i] == '`' {
			q := src[i]
			i++
			start := i
			for i < len(src) && src[i] != q {
				if src[i] == '\\' && i+1 < len(src) {
					i += 2
					continue
				}
				i++
			}
			if i < len(src) {
				out = append(out, src[start:i])
				i++
			} else {
				break
			}
			continue
		}
		i++
	}
	return out
}

// parseSQL recognises CREATE TABLE / INSERT / UPDATE / SELECT in the
// given chunk. Returns one statement per recognised top-level
// keyword; we do not handle multi-statement scripts beyond walking
// CREATE TABLE blocks one at a time.
func parseSQL(s string) []sqlStatement {
	out := parseCreateTables(s)
	out = append(out, parseInserts(s)...)
	out = append(out, parseUpdates(s)...)
	out = append(out, parseSelects(s)...)
	return out
}

// --- CREATE TABLE ---------------------------------------------------------

var createTableRE = regexp.MustCompile(`(?is)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:"([^"]+)"|` + "`" + `([^` + "`" + `]+)` + "`" + `|([a-zA-Z_][a-zA-Z0-9_\.]*))\s*\(`)

func parseCreateTables(s string) []sqlStatement {
	var out []sqlStatement
	for _, m := range createTableRE.FindAllStringSubmatchIndex(s, -1) {
		var table string
		for g := 1; g <= 3; g++ {
			if m[2*g] >= 0 {
				table = s[m[2*g]:m[2*g+1]]
				break
			}
		}
		// Find matching `)` for the column-list opening `(` at m[1]-1.
		open := m[1] - 1
		close := findMatchingParen(s, open)
		if close < 0 {
			continue
		}
		columns := parseCreateColumns(s[open+1 : close])
		// Strip schema qualifier for the table name (e.g. `public.foo`).
		if i := strings.LastIndexByte(table, '.'); i >= 0 {
			table = table[i+1:]
		}
		out = append(out, sqlStatement{kind: stmtCreateTable, table: table, columns: columns})
	}
	return out
}

// parseCreateColumns splits the column list on top-level commas and
// peels each row into (name, type, nullable). Constraints like
// PRIMARY KEY, FOREIGN KEY, CONSTRAINT ... at the top level are
// skipped.
func parseCreateColumns(body string) []sqlColumn {
	parts := splitTopLevel(body, ',')
	var out []sqlColumn
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		upper := strings.ToUpper(p)
		if strings.HasPrefix(upper, "PRIMARY KEY") ||
			strings.HasPrefix(upper, "FOREIGN KEY") ||
			strings.HasPrefix(upper, "UNIQUE") ||
			strings.HasPrefix(upper, "CONSTRAINT") ||
			strings.HasPrefix(upper, "CHECK") ||
			strings.HasPrefix(upper, "INDEX") ||
			strings.HasPrefix(upper, "KEY") {
			continue
		}
		// First token = name; second = type. Handle quoted names.
		name, rest := nextIdent(p)
		typ, _ := nextIdent(rest)
		notnull := strings.Contains(upper, "NOT NULL")
		nullable := !notnull && !strings.Contains(upper, "PRIMARY KEY")
		out = append(out, sqlColumn{name: name, dataType: typ, nullable: nullable})
	}
	return out
}

// nextIdent peels one identifier off the front of s and returns it +
// the remaining string. Quoted ("..." / `...`) names are honoured.
func nextIdent(s string) (string, string) {
	s = strings.TrimLeft(s, " \t\n\r")
	if s == "" {
		return "", ""
	}
	if s[0] == '"' {
		end := strings.IndexByte(s[1:], '"')
		if end < 0 {
			return s[1:], ""
		}
		return s[1 : 1+end], s[2+end:]
	}
	if s[0] == '`' {
		end := strings.IndexByte(s[1:], '`')
		if end < 0 {
			return s[1:], ""
		}
		return s[1 : 1+end], s[2+end:]
	}
	end := 0
	for end < len(s) {
		c := s[end]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '(' || c == ',' {
			break
		}
		end++
	}
	return s[:end], s[end:]
}

// --- INSERT INTO ---------------------------------------------------------

var insertRE = regexp.MustCompile(`(?is)\bINSERT\s+INTO\s+(?:"([^"]+)"|` + "`" + `([^` + "`" + `]+)` + "`" + `|([a-zA-Z_][a-zA-Z0-9_\.]*))\s*(?:\(([^)]*)\))?`)

func parseInserts(s string) []sqlStatement {
	var out []sqlStatement
	for _, m := range insertRE.FindAllStringSubmatch(s, -1) {
		var table string
		for i := 1; i <= 3; i++ {
			if m[i] != "" {
				table = m[i]
				break
			}
		}
		if i := strings.LastIndexByte(table, '.'); i >= 0 {
			table = table[i+1:]
		}
		var cols []sqlColumn
		for _, raw := range strings.Split(m[4], ",") {
			c, _ := nextIdent(strings.TrimSpace(raw))
			if c != "" {
				cols = append(cols, sqlColumn{name: c})
			}
		}
		out = append(out, sqlStatement{kind: stmtInsert, table: table, columns: cols})
	}
	return out
}

// --- UPDATE ---------------------------------------------------------------

var updateRE = regexp.MustCompile(`(?is)\bUPDATE\s+(?:"([^"]+)"|` + "`" + `([^` + "`" + `]+)` + "`" + `|([a-zA-Z_][a-zA-Z0-9_\.]*))\s+SET\s+(.*?)(?:\s+WHERE\b|\s*;|\s*$)`)

// setColRE captures one `col = expr` pair from the SET clause.
var setColRE = regexp.MustCompile(`(?:"([^"]+)"|` + "`" + `([^` + "`" + `]+)` + "`" + `|([a-zA-Z_][a-zA-Z0-9_]*))\s*=`)

func parseUpdates(s string) []sqlStatement {
	var out []sqlStatement
	for _, m := range updateRE.FindAllStringSubmatch(s, -1) {
		var table string
		for i := 1; i <= 3; i++ {
			if m[i] != "" {
				table = m[i]
				break
			}
		}
		if i := strings.LastIndexByte(table, '.'); i >= 0 {
			table = table[i+1:]
		}
		var cols []sqlColumn
		for _, cm := range setColRE.FindAllStringSubmatch(m[4], -1) {
			var c string
			for i := 1; i <= 3; i++ {
				if cm[i] != "" {
					c = cm[i]
					break
				}
			}
			if c != "" {
				cols = append(cols, sqlColumn{name: c})
			}
		}
		out = append(out, sqlStatement{kind: stmtUpdate, table: table, columns: cols})
	}
	return out
}

// --- SELECT ---------------------------------------------------------------

var selectRE = regexp.MustCompile(`(?is)\bSELECT\s+(.+?)\s+FROM\s+(?:"([^"]+)"|` + "`" + `([^` + "`" + `]+)` + "`" + `|([a-zA-Z_][a-zA-Z0-9_\.]*))`)

func parseSelects(s string) []sqlStatement {
	var out []sqlStatement
	for _, m := range selectRE.FindAllStringSubmatch(s, -1) {
		colsList := strings.TrimSpace(m[1])
		var table string
		for i := 2; i <= 4; i++ {
			if m[i] != "" {
				table = m[i]
				break
			}
		}
		if i := strings.LastIndexByte(table, '.'); i >= 0 {
			table = table[i+1:]
		}
		// "*" — emit zero columns; we only know the table here.
		if colsList == "*" {
			out = append(out, sqlStatement{kind: stmtSelect, table: table})
			continue
		}
		var cols []sqlColumn
		for _, raw := range splitTopLevel(colsList, ',') {
			c := strings.TrimSpace(raw)
			// Strip aliases (" AS foo") and table-prefixed names ("u.id").
			if idx := strings.Index(strings.ToUpper(c), " AS "); idx >= 0 {
				c = c[:idx]
			}
			if idx := strings.IndexByte(c, ' '); idx >= 0 {
				c = c[:idx]
			}
			if idx := strings.LastIndexByte(c, '.'); idx >= 0 {
				c = c[idx+1:]
			}
			if c == "" || c == "*" {
				continue
			}
			cols = append(cols, sqlColumn{name: strings.Trim(c, "\"`")})
		}
		out = append(out, sqlStatement{kind: stmtSelect, table: table, columns: cols})
	}
	return out
}

// --- helpers ---------------------------------------------------------------

// findMatchingParen returns the index of the `)` matching the `(` at
// openIdx, or -1 if no balanced pair is found.
func findMatchingParen(s string, openIdx int) int {
	depth := 1
	for i := openIdx + 1; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		case '"', '\'':
			q := s[i]
			i++
			for i < len(s) && s[i] != q {
				if s[i] == '\\' && i+1 < len(s) {
					i += 2
					continue
				}
				i++
			}
		}
	}
	return -1
}

// splitTopLevel splits s on the separator at depth-0 only (parens
// honoured). Used to peel comma-separated column lists.
func splitTopLevel(s string, sep byte) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

func init() {
	cf.Register(Name, New, cf.Descriptor{
		Family:     "schemas",
		Languages:  []string{"go", "typescript", "python"},
		Frameworks: []string{"raw-sql"},
		Fallback:   cf.FallbackLossy,
		BatchHint:  cf.BatchPerEvent,
		Inputs:     []cf.EventKind{cf.InputCoreFileChanged},
		Outputs:    []cf.EntityKind{cf.KindSchema, cf.KindSchemaField, cf.KindSchemaRead, cf.KindSchemaWrite},
	})
}
