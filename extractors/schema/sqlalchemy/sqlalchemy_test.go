package sqlalchemy

import (
	"encoding/json"
	"testing"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
)

const fixtureClassical = `from sqlalchemy import Column, Integer, String, DateTime, ForeignKey
from sqlalchemy.orm import declarative_base, relationship

Base = declarative_base()

class User(Base):
    __tablename__ = "users"
    id = Column(Integer, primary_key=True)
    email = Column(String(255), nullable=False)
    name = Column(String(255), nullable=True)
    created_at = Column(DateTime)
    posts = relationship("Post", back_populates="author")


class Post(Base):
    __tablename__ = "posts"
    id = Column(Integer, primary_key=True)
    title = Column(String(255), nullable=False)
    author_id = Column(Integer, ForeignKey("users.id"), nullable=False)
`

func TestParse_SQLAlchemyClassical(t *testing.T) {
	events := Parse("app/models.py", []byte(fixtureClassical))
	if len(events) == 0 {
		t.Fatalf("no events")
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
			if s.Dialect != "sqlalchemy" {
				t.Errorf("dialect %q want sqlalchemy", s.Dialect)
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
	// User: 4 columns (id, email, name, created_at) — `posts` is a
	// relationship and must be skipped. Post: 3 columns.
	if fields != 7 {
		t.Errorf("fields %d want 7", fields)
	}
	if !tables["users"] || !tables["posts"] {
		t.Errorf("missing tables: %v", tables)
	}
	if nullables["email"] {
		t.Errorf("email should be NOT nullable")
	}
	if !nullables["name"] {
		t.Errorf("name should be nullable")
	}
	if nullables["id"] {
		t.Errorf("id (primary_key) should be NOT nullable")
	}
}

const fixtureMapped = `from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column
from typing import Optional

class Base(DeclarativeBase):
    pass

class Item(Base):
    __tablename__ = "items"
    id: Mapped[int] = mapped_column(primary_key=True)
    name: Mapped[str] = mapped_column(nullable=False)
    note: Mapped[Optional[str]] = mapped_column()
`

func TestParse_SQLAlchemyMapped(t *testing.T) {
	events := Parse("app/m.py", []byte(fixtureMapped))
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
		t.Errorf("schemas %d want 1 (Base class with no __tablename__ skipped)", schemas)
	}
	if fields != 3 {
		t.Errorf("fields %d want 3", fields)
	}
}

func TestParse_NonModelClass(t *testing.T) {
	src := `class Service:
    def __init__(self):
        self.x = 1
`
	if events := Parse("a.py", []byte(src)); len(events) != 0 {
		t.Errorf("non-model class emitted %d events", len(events))
	}
}
