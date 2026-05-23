package prisma

import (
	"encoding/json"
	"testing"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
)

const fixtureSchema = `// Prisma schema fixture
datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

generator client {
  provider = "prisma-client-js"
}

model User {
  id        Int      @id @default(autoincrement())
  email     String   @unique
  name      String?
  createdAt DateTime @default(now())
  posts     Post[]
}

model Post {
  id       Int    @id @default(autoincrement())
  title    String
  authorId Int
}
`

func TestParse_PrismaFixture(t *testing.T) {
	events := Parse("prisma/schema.prisma", []byte(fixtureSchema))
	if len(events) == 0 {
		t.Fatalf("Parse returned 0 events; want >0")
	}

	var schemas, fields int
	tablesByID := map[string]string{}
	for _, ev := range events {
		switch ev.Kind {
		case "SchemaAdded":
			schemas++
			var s cf.Schema
			if err := json.Unmarshal(ev.Payload, &s); err != nil {
				t.Fatalf("decode Schema: %v", err)
			}
			if s.Dialect != "prisma" {
				t.Errorf("dialect: got %q want prisma", s.Dialect)
			}
			tablesByID[s.ID] = s.Table
		case "SchemaFieldAdded":
			fields++
		default:
			t.Errorf("unexpected event kind %q", ev.Kind)
		}
	}
	if schemas != 2 {
		t.Errorf("schemas: got %d want 2", schemas)
	}
	// User has 4 scalar fields (id,email,name,createdAt) — `posts` is a
	// relation and must be skipped. Post has 3 scalar fields.
	if fields != 7 {
		t.Errorf("fields: got %d want 7", fields)
	}
}

func TestParse_EmptyAndNonModel(t *testing.T) {
	// File with no `model` blocks should yield nothing.
	events := Parse("schema.prisma", []byte(`datasource db { provider = "postgres" }`))
	if len(events) != 0 {
		t.Errorf("non-model file emitted %d events; want 0", len(events))
	}
}

func TestParse_NullableDetection(t *testing.T) {
	src := `model T {
  id   Int
  name String?
}`
	events := Parse("schema.prisma", []byte(src))
	var sawNullable, sawNonNullable bool
	for _, ev := range events {
		if ev.Kind != "SchemaFieldAdded" {
			continue
		}
		var f cf.SchemaField
		_ = json.Unmarshal(ev.Payload, &f)
		switch f.Name {
		case "id":
			sawNonNullable = !f.Nullable
		case "name":
			sawNullable = f.Nullable
		}
	}
	if !sawNullable {
		t.Errorf("expected name to be nullable")
	}
	if !sawNonNullable {
		t.Errorf("expected id to be non-nullable")
	}
}
