package prismamigrate

import (
	"encoding/json"
	"testing"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
)

func TestParse_PrismaMigrate(t *testing.T) {
	rel := "prisma/migrations/20240101120000_create_users/migration.sql"
	events := Parse(rel)
	if len(events) != 1 {
		t.Fatalf("events: got %d want 1", len(events))
	}
	var m cf.Migration
	_ = json.Unmarshal(events[0].Payload, &m)
	if m.Tool != "prisma-migrate" {
		t.Errorf("tool %q want prisma-migrate", m.Tool)
	}
	if m.Version != "20240101120000" {
		t.Errorf("version %q want 20240101120000", m.Version)
	}
}

func TestParse_NotPrismaPath(t *testing.T) {
	if events := Parse("migrations/2024_x/migration.sql"); len(events) != 1 {
		t.Errorf("expected detection for any /migrations/<ts>_<name>/migration.sql path; got %d", len(events))
	}
	if events := Parse("src/Foo.sql"); len(events) != 0 {
		t.Errorf("expected 0 for non-migration sql file; got %d", len(events))
	}
	if events := Parse("prisma/migrations/foo/migration.sql"); len(events) != 0 {
		t.Errorf("expected 0 for dir without leading digits; got %d", len(events))
	}
}
