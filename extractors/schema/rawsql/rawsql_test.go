package rawsql

import (
	"encoding/json"
	"testing"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
)

const fixtureSQL = `-- raw SQL DDL fixture
CREATE TABLE IF NOT EXISTS users (
  id        SERIAL PRIMARY KEY,
  email     VARCHAR(255) NOT NULL,
  name      VARCHAR(255),
  created_at TIMESTAMP NOT NULL DEFAULT NOW(),
  UNIQUE (email)
);

INSERT INTO users (email, name) VALUES ('a@b.com', 'A');
UPDATE users SET name = 'B', email = 'c@d.com' WHERE id = 1;
SELECT id, email FROM users WHERE id = 1;
`

func TestParse_RawSQLFile(t *testing.T) {
	events := Parse("migrations/001.sql", []byte(fixtureSQL))
	if len(events) == 0 {
		t.Fatalf("no events")
	}
	var schemas, fields, reads, writes int
	for _, ev := range events {
		switch ev.Kind {
		case "SchemaAdded":
			schemas++
			var s cf.Schema
			_ = json.Unmarshal(ev.Payload, &s)
			if s.Table != "users" {
				t.Errorf("table %q want users", s.Table)
			}
			if s.Dialect != "sql" {
				t.Errorf("dialect %q want sql", s.Dialect)
			}
		case "SchemaFieldAdded":
			fields++
		case "SchemaReadAdded":
			reads++
		case "SchemaWriteAdded":
			writes++
		}
	}
	if schemas != 1 {
		t.Errorf("schemas %d want 1", schemas)
	}
	// 4 columns from CREATE TABLE; UNIQUE clause is skipped.
	if fields != 4 {
		t.Errorf("fields %d want 4", fields)
	}
	// INSERT touches 2 cols + UPDATE touches 2 cols = 4 writes.
	if writes != 4 {
		t.Errorf("writes %d want 4", writes)
	}
	// SELECT picks 2 cols.
	if reads != 2 {
		t.Errorf("reads %d want 2", reads)
	}
}

func TestParse_EmbeddedSQLInGo(t *testing.T) {
	src := "package x\n\nfunc q() string { return `SELECT id, email FROM users WHERE id = $1` }\n"
	events := Parse("x.go", []byte(src))
	var reads int
	for _, ev := range events {
		if ev.Kind == "SchemaReadAdded" {
			reads++
		}
	}
	if reads != 2 {
		t.Errorf("embedded SQL reads %d want 2", reads)
	}
}

func TestParse_TripleQuotedPython(t *testing.T) {
	src := `def q():
    return """
        CREATE TABLE foo (id INT NOT NULL, name TEXT)
    """
`
	events := Parse("q.py", []byte(src))
	var schemas, fields int
	for _, ev := range events {
		switch ev.Kind {
		case "SchemaAdded":
			schemas++
		case "SchemaFieldAdded":
			fields++
		}
	}
	if schemas != 1 {
		t.Errorf("py triple-quoted: schemas %d want 1", schemas)
	}
	if fields != 2 {
		t.Errorf("py triple-quoted: fields %d want 2", fields)
	}
}

func TestParse_NoSQL(t *testing.T) {
	src := `console.log("hello world"); const x = "no sql here";`
	if events := Parse("a.ts", []byte(src)); len(events) != 0 {
		t.Errorf("non-sql ts emitted %d events", len(events))
	}
}
