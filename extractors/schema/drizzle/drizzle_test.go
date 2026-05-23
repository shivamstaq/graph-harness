package drizzle

import (
	"encoding/json"
	"testing"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
)

const fixtureDrizzle = `// Drizzle schema fixture
import { pgTable, serial, text, integer, timestamp } from "drizzle-orm/pg-core";

export const users = pgTable("users", {
  id: serial("id").primaryKey(),
  email: text("email").notNull(),
  name: text("name"),
  createdAt: timestamp("created_at").notNull().defaultNow(),
});

export const posts = pgTable("posts", {
  id: serial("id").primaryKey(),
  title: text("title").notNull(),
  authorId: integer("author_id").notNull(),
});
`

func TestParse_DrizzleFixture(t *testing.T) {
	events := Parse("src/schema.ts", []byte(fixtureDrizzle))
	if len(events) == 0 {
		t.Fatalf("Parse returned 0 events")
	}
	var schemas, fields int
	tables := map[string]bool{}
	nullables := map[string]bool{}
	for _, ev := range events {
		switch ev.Kind {
		case "SchemaAdded":
			schemas++
			var s cf.Schema
			_ = json.Unmarshal(ev.Payload, &s)
			tables[s.Table] = true
			if s.Dialect != "drizzle" {
				t.Errorf("dialect %q want drizzle", s.Dialect)
			}
		case "SchemaFieldAdded":
			fields++
			var f cf.SchemaField
			_ = json.Unmarshal(ev.Payload, &f)
			nullables[f.Name] = f.Nullable
		}
	}
	if schemas != 2 {
		t.Errorf("schemas %d want 2", schemas)
	}
	if fields != 7 {
		t.Errorf("fields %d want 7", fields)
	}
	if !tables["users"] || !tables["posts"] {
		t.Errorf("missing expected tables; saw %v", tables)
	}
	// `name` is nullable (no .notNull()); `email` is not.
	if !nullables["name"] {
		t.Errorf("expected name nullable")
	}
	if nullables["email"] {
		t.Errorf("expected email NOT nullable")
	}
}

func TestParse_NoDrizzle(t *testing.T) {
	src := `console.log("hi"); export const x = 1;`
	if events := Parse("a.ts", []byte(src)); len(events) != 0 {
		t.Errorf("non-drizzle ts emitted %d events", len(events))
	}
}

func TestParse_MysqlAndSqliteTables(t *testing.T) {
	src := `import { mysqlTable, sqliteTable, int, text } from "drizzle-orm";
export const a = mysqlTable("a", { id: int("id").primaryKey() });
export const b = sqliteTable("b", { id: int("id").primaryKey(), n: text("n") });`
	events := Parse("s.ts", []byte(src))
	schemas := 0
	for _, ev := range events {
		if ev.Kind == "SchemaAdded" {
			schemas++
		}
	}
	if schemas != 2 {
		t.Errorf("schemas %d want 2", schemas)
	}
}
