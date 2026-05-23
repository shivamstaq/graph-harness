package alembic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
)

func TestParse_AlembicMigration(t *testing.T) {
	dir := t.TempDir()
	rel := "alembic/versions/abc123def_create_users.py"
	full := filepath.Join(dir, rel)
	_ = os.MkdirAll(filepath.Dir(full), 0o755)
	body := `"""create users

Revision ID: abc123def
"""
revision = "abc123def"
down_revision = None

def upgrade():
    pass

def downgrade():
    pass
`
	_ = os.WriteFile(full, []byte(body), 0o644)

	events := Parse(dir, rel)
	if len(events) != 1 {
		t.Fatalf("events: got %d want 1", len(events))
	}
	var m cf.Migration
	_ = json.Unmarshal(events[0].Payload, &m)
	if m.Tool != "alembic" {
		t.Errorf("tool %q want alembic", m.Tool)
	}
	if m.Version != "abc123def" {
		t.Errorf("version %q want abc123def", m.Version)
	}
}

func TestParse_NonVersionsFolder(t *testing.T) {
	dir := t.TempDir()
	rel := "models/abc_user.py"
	full := filepath.Join(dir, rel)
	_ = os.MkdirAll(filepath.Dir(full), 0o755)
	_ = os.WriteFile(full, []byte(`revision = "x"`), 0o644)
	if events := Parse(dir, rel); len(events) != 0 {
		t.Errorf("expected 0 events outside versions/")
	}
}

func TestParse_NoUpgradeNoRevision(t *testing.T) {
	dir := t.TempDir()
	rel := "alembic/versions/abc_x.py"
	full := filepath.Join(dir, rel)
	_ = os.MkdirAll(filepath.Dir(full), 0o755)
	_ = os.WriteFile(full, []byte(`def hi(): pass`), 0o644)
	if events := Parse(dir, rel); len(events) != 0 {
		t.Errorf("expected 0 events for file without revision/upgrade")
	}
}
