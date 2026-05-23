package drizzlekit

import (
	"encoding/json"
	"testing"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
)

func TestParse_DrizzleKitMigration(t *testing.T) {
	events := Parse("drizzle/0001_init.sql")
	if len(events) != 1 {
		t.Fatalf("events: got %d want 1", len(events))
	}
	var m cf.Migration
	_ = json.Unmarshal(events[0].Payload, &m)
	if m.Tool != "drizzle-kit" {
		t.Errorf("tool %q want drizzle-kit", m.Tool)
	}
	if m.Version != "0001" {
		t.Errorf("version %q want 0001", m.Version)
	}
}

func TestParse_DrizzleKitMetaSkipped(t *testing.T) {
	if events := Parse("drizzle/meta/_journal.json"); len(events) != 0 {
		t.Errorf("expected meta/ files to be skipped")
	}
}

func TestParse_NonDrizzlePath(t *testing.T) {
	if events := Parse("migrations/0001_init.sql"); len(events) != 0 {
		t.Errorf("expected 0 events outside drizzle/ folder")
	}
}
