package gorm

import (
	"encoding/json"
	"testing"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
)

const fixtureGorm = `package models

import "time"

type User struct {
	ID        uint   ` + "`gorm:\"primaryKey\"`" + `
	Email     string ` + "`gorm:\"column:email;not null\"`" + `
	Name      *string ` + "`gorm:\"column:name\"`" + `
	CreatedAt time.Time ` + "`gorm:\"column:created_at\"`" + `
	Internal  string ` + "`gorm:\"-\"`" + `
}

func (User) TableName() string { return "users" }

type Post struct {
	ID       uint   ` + "`gorm:\"primaryKey\"`" + `
	Title    string ` + "`gorm:\"not null\"`" + `
	AuthorID uint   ` + "`gorm:\"column:author_id\"`" + `
}

type NotAModel struct {
	X int
	Y string
}
`

func TestParse_GormFixture(t *testing.T) {
	events := Parse("models/models.go", []byte(fixtureGorm))
	if len(events) == 0 {
		t.Fatalf("no events")
	}
	var schemas, fields int
	tables := map[string]bool{}
	cols := map[string]bool{}
	nullable := map[string]bool{}
	for _, ev := range events {
		switch ev.Kind {
		case "SchemaAdded":
			schemas++
			var s cf.Schema
			_ = json.Unmarshal(ev.Payload, &s)
			tables[s.Table] = true
			if s.Dialect != "gorm" {
				t.Errorf("dialect %q want gorm", s.Dialect)
			}
		case "SchemaFieldAdded":
			fields++
			var f cf.SchemaField
			_ = json.Unmarshal(ev.Payload, &f)
			cols[f.Name] = true
			nullable[f.Name] = f.Nullable
		}
	}
	if schemas != 2 {
		t.Errorf("schemas %d want 2 (Post + User; NotAModel skipped)", schemas)
	}
	// User has 4 mapped columns (Internal skipped because tag is `-`).
	// Post has 3.
	if fields != 7 {
		t.Errorf("fields %d want 7", fields)
	}
	if !tables["users"] {
		t.Errorf("expected 'users' table from TableName override; saw %v", tables)
	}
	if !tables["posts"] {
		t.Errorf("expected default 'posts' table; saw %v", tables)
	}
	if !cols["name"] {
		t.Errorf("expected 'name' column")
	}
	if !nullable["name"] {
		t.Errorf("expected *string Name to be nullable")
	}
	if nullable["email"] {
		t.Errorf("expected email to be NOT nullable (not null tag)")
	}
}

const fixtureEmbedded = `package m

import "gorm.io/gorm"

type Item struct {
	gorm.Model
	SKU  string ` + "`gorm:\"uniqueIndex;not null\"`" + `
}
`

func TestParse_GormModelEmbed(t *testing.T) {
	events := Parse("m/item.go", []byte(fixtureEmbedded))
	var fields int
	for _, ev := range events {
		if ev.Kind == "SchemaFieldAdded" {
			fields++
		}
	}
	// 4 from gorm.Model + 1 SKU.
	if fields != 5 {
		t.Errorf("embedded model fields %d want 5", fields)
	}
}

func TestParse_NotAModel(t *testing.T) {
	src := `package x

type Foo struct {
	A int
	B string
}
`
	if events := Parse("x.go", []byte(src)); len(events) != 0 {
		t.Errorf("non-model emitted %d events", len(events))
	}
}

func TestSnakeCase_AcronymBoundaries(t *testing.T) {
	cases := map[string]string{
		"ID":         "id",
		"UserID":     "user_id",
		"AuthorID":   "author_id",
		"CreatedAt":  "created_at",
		"HTTPServer": "http_server",
		"Name":       "name",
		"URL":        "url",
		"APIKey":     "api_key",
	}
	for in, want := range cases {
		if got := snakeCase(in); got != want {
			t.Errorf("snakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}
