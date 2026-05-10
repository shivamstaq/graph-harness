package detect

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// WorkspaceConfig is the parsed shape of `.graph-harness/config.toml`,
// the per-workspace tooling override file (P1.L T55; SPEC §6.18). The
// only knob in v1 is `[lsp.<lang>] server = "..."` — telling
// graph-harness to prefer an alternative LSP server (e.g.
// basedpyright over pyright). Future P2+ knobs (probe-path overrides,
// version pins) extend this struct without breaking existing files.
type WorkspaceConfig struct {
	LSP map[string]LSPServerOverride `toml:"lsp"`
}

// LSPServerOverride configures the canonical LSP server choice for a
// single language id.
type LSPServerOverride struct {
	// Server is the canonical id of the LSP server to use for this
	// language id. Must match a known server in the registry — unknown
	// values are ignored with a warning so a typo doesn't silently
	// disable LSP.
	Server string `toml:"server"`
}

// LoadWorkspaceConfig reads the workspace's config file (if any) and
// returns the parsed config. A missing file returns an empty config
// without error — overrides are optional. Parse errors are returned
// so the user sees them.
func LoadWorkspaceConfig(workspaceRoot string) (*WorkspaceConfig, error) {
	if workspaceRoot == "" {
		return &WorkspaceConfig{}, nil
	}
	path := filepath.Join(workspaceRoot, ".graph-harness", "config.toml")
	data, err := os.ReadFile(path) //nolint:gosec // workspaceRoot is caller-provided; path is fixed
	if os.IsNotExist(err) {
		return &WorkspaceConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg WorkspaceConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// ServerFor returns the canonical LSP server id the workspace prefers
// for languageID, or "" when no override is configured. Caller falls
// back to the registry default in that case.
func (c *WorkspaceConfig) ServerFor(languageID string) string {
	if c == nil || c.LSP == nil {
		return ""
	}
	o, ok := c.LSP[languageID]
	if !ok {
		return ""
	}
	return o.Server
}

// KnownLSPServers is the registry of LSP server ids the workspace
// config can resolve against. P1 ships canonical defaults only; the
// alternatives are listed so the override mechanism resolves a real
// probe target rather than an arbitrary executable name. P2 graduates
// the alternatives once they have e2e coverage.
var KnownLSPServers = map[string][]string{
	"go":         {"gopls"},
	"typescript": {"typescript-language-server", "vtsls"},
	"python":     {"pyright", "basedpyright", "pylsp", "jedi-language-server"},
}

// IsKnownServer reports whether serverID is registered as a valid LSP
// server choice for languageID. Used by the config loader to warn on
// typos.
func IsKnownServer(languageID, serverID string) bool {
	known, ok := KnownLSPServers[languageID]
	if !ok {
		return false
	}
	for _, s := range known {
		if s == serverID {
			return true
		}
	}
	return false
}
