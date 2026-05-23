package goose

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
)

func TestParse_GooseSQLMigration(t *testing.T) {
	dir := t.TempDir()
	rel := "migrations/20240101120000_create_users.sql"
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `-- +goose Up
CREATE TABLE users (id SERIAL PRIMARY KEY);

-- +goose Down
DROP TABLE users;
`
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	events := Parse(dir, rel)
	if len(events) != 1 {
		t.Fatalf("events: got %d want 1", len(events))
	}
	var m cf.Migration
	if err := json.Unmarshal(events[0].Payload, &m); err != nil {
		t.Fatal(err)
	}
	if m.Tool != "goose" {
		t.Errorf("tool %q want goose", m.Tool)
	}
	if m.Version != "20240101120000" {
		t.Errorf("version %q want 20240101120000", m.Version)
	}
}

func TestParse_GooseGoMigration(t *testing.T) {
	dir := t.TempDir()
	rel := "migrations/00007_seed.go"
	full := filepath.Join(dir, rel)
	_ = os.MkdirAll(filepath.Dir(full), 0o755)
	body := `package migrations
import "github.com/pressly/goose/v3"
func init() { goose.AddMigration(up, down) }
func up() error { return nil }
func down() error { return nil }
`
	_ = os.WriteFile(full, []byte(body), 0o644)
	events := Parse(dir, rel)
	if len(events) != 1 {
		t.Fatalf("got %d events want 1", len(events))
	}
	var m cf.Migration
	_ = json.Unmarshal(events[0].Payload, &m)
	if m.Version != "00007" {
		t.Errorf("version %q want 00007", m.Version)
	}
}

func TestParse_NotGoose(t *testing.T) {
	dir := t.TempDir()
	rel := "migrations/001_no_marker.sql"
	full := filepath.Join(dir, rel)
	_ = os.MkdirAll(filepath.Dir(full), 0o755)
	_ = os.WriteFile(full, []byte("SELECT 1;"), 0o644)
	if events := Parse(dir, rel); len(events) != 0 {
		t.Errorf("expected 0 events for SQL without +goose marker; got %d", len(events))
	}
}

func TestParse_WrongFilename(t *testing.T) {
	if events := Parse(t.TempDir(), "not-a-migration.sql"); len(events) != 0 {
		t.Errorf("expected 0 events for non-goose filename")
	}
}
