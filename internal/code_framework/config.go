package code_framework

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"

	"github.com/BurntSushi/toml"
)

// configFileRelPath is the workspace-relative path of the
// per-extractor enable/disable file. Lives under .graph-harness/ so
// the existing graph-harness.toml stays workspace-level
// (one toml = one concern).
const configFileRelPath = ".graph-harness/extractors.toml"

// Config is the on-disk per-extractor enable/disable record.
type Config struct {
	// Disabled lists extractor names that should NOT be started by
	// the Dispatcher even though they are registered. Anything not
	// listed is implicitly enabled.
	Disabled []string `toml:"disabled,omitempty"`

	// GeneratedPatterns lists workspace-declared glob patterns the
	// generated-artifacts extractor (P2.T29) should treat as
	// generated. Added here rather than in graph-harness.toml so
	// the entire extractor concern lives in one file.
	GeneratedPatterns []string `toml:"generated_patterns,omitempty"`
}

// LoadConfig reads .graph-harness/extractors.toml from workspaceRoot.
// Returns a zero-value Config (everything enabled) if the file does
// not exist; an error if it exists but is malformed.
func LoadConfig(workspaceRoot string) (*Config, error) {
	path := filepath.Join(workspaceRoot, configFileRelPath)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("read extractors.toml: %w", err)
	}
	var c Config
	if err := toml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse extractors.toml: %w", err)
	}
	return &c, nil
}

// SaveConfig writes the config back to disk atomically (tempfile +
// rename). Creates the .graph-harness/ dir if missing.
func SaveConfig(workspaceRoot string, cfg *Config) error {
	dir := filepath.Join(workspaceRoot, ".graph-harness")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir .graph-harness: %w", err)
	}
	// Canonicalize: dedupe + sort disabled list so the file is
	// reproducible across writes.
	cfg = cfg.canon()
	tmp, err := os.CreateTemp(dir, "extractors-*.toml.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	defer os.Remove(tmp.Name())
	enc := toml.NewEncoder(tmp)
	if err := enc.Encode(cfg); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encode toml: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	final := filepath.Join(workspaceRoot, configFileRelPath)
	if err := os.Rename(tmp.Name(), final); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// canon returns a normalized copy: dedup + sorted lists, no nil
// slices in the output.
func (c *Config) canon() *Config {
	out := &Config{
		Disabled:          dedupSorted(c.Disabled),
		GeneratedPatterns: dedupSorted(c.GeneratedPatterns),
	}
	return out
}

// IsDisabled reports whether the named extractor is in the disabled
// list. Goroutine-safe (callers may invoke from any goroutine).
func (c *Config) IsDisabled(name string) bool {
	return slices.Contains(c.Disabled, name)
}

// Disable adds name to the disabled list (idempotent).
func (c *Config) Disable(name string) {
	if c.IsDisabled(name) {
		return
	}
	c.Disabled = append(c.Disabled, name)
}

// Enable removes name from the disabled list (idempotent).
func (c *Config) Enable(name string) {
	out := c.Disabled[:0]
	for _, d := range c.Disabled {
		if d != name {
			out = append(out, d)
		}
	}
	c.Disabled = out
}

// configMu serializes Save calls so concurrent CLI invocations don't
// race. The Dispatcher uses one mutex per process; cross-process
// races are out of scope (writes flow through the daemon's single-
// writer path per P0.5).
var configMu sync.Mutex

// WithLockedConfig runs fn against the workspace's extractors.toml
// under a process-wide mutex. Reads + writes happen atomically so
// JSON-RPC handlers don't corrupt the file under concurrent
// enable/disable calls.
func WithLockedConfig(workspaceRoot string, fn func(*Config) error) error {
	configMu.Lock()
	defer configMu.Unlock()
	cfg, err := LoadConfig(workspaceRoot)
	if err != nil {
		return err
	}
	if err := fn(cfg); err != nil {
		return err
	}
	return SaveConfig(workspaceRoot, cfg)
}

func dedupSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
