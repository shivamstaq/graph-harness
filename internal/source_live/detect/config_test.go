package detect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWorkspaceConfig_MissingReturnsEmpty(t *testing.T) {
	cfg, err := LoadWorkspaceConfig(t.TempDir())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cfg.ServerFor("python") != "" {
		t.Fatalf("expected empty override")
	}
}

func TestLoadWorkspaceConfig_ParsesLSPOverrides(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".graph-harness"), 0o750); err != nil {
		t.Fatal(err)
	}
	body := `
[lsp.python]
server = "basedpyright"

[lsp.typescript]
server = "vtsls"
`
	if err := os.WriteFile(filepath.Join(root, ".graph-harness", "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWorkspaceConfig(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.ServerFor("python"); got != "basedpyright" {
		t.Fatalf("python: %q", got)
	}
	if got := cfg.ServerFor("typescript"); got != "vtsls" {
		t.Fatalf("typescript: %q", got)
	}
	if got := cfg.ServerFor("go"); got != "" {
		t.Fatalf("go: %q (no override expected)", got)
	}
}

func TestIsKnownServer(t *testing.T) {
	if !IsKnownServer("python", "basedpyright") {
		t.Fatal("basedpyright should be known for python")
	}
	if IsKnownServer("python", "made-up-server") {
		t.Fatal("made-up-server should not be known")
	}
	if IsKnownServer("rust", "rust-analyzer") {
		t.Fatal("rust not yet registered")
	}
}
