package manifest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// fileChangedEvent builds a synthetic code.core.FileChanged event for
// the given relative path.
func fileChangedEvent(t *testing.T, path string) kernel.Event {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return kernel.Event{Layer: "code.core", Kind: "FileChanged", Payload: payload}
}

// newTestExtractor constructs the extractor with patterns injected via
// the loadConfig override. Avoids writing extractors.toml to disk for
// most tests; the seedExtractorsToml test exercises the real loader.
func newTestExtractor(t *testing.T, patterns []string) *Extractor {
	t.Helper()
	e := &Extractor{logf: t.Logf}
	e.loadConfig = func() ([]string, error) { return patterns, nil }
	if err := e.refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	return e
}

func TestManifestExtractor_BasicMatch(t *testing.T) {
	ext := newTestExtractor(t, []string{
		"*.gen.zig",
		"vendor/embedded/*.go",
		"schemas/*.generated.json",
		"i18n_translations.py",
	})

	cases := []struct {
		name        string
		path        string
		wantMatch   bool
		wantGlob    string
	}{
		{"basename glob match (Zig)", "thing.gen.zig", true, "*.gen.zig"},
		{"path-glob match", "vendor/embedded/foo.go", true, "vendor/embedded/*.go"},
		{"nested path glob mismatch falls through", "vendor/embedded/sub/foo.go", false, ""},
		{"exact filename match", "i18n_translations.py", true, "i18n_translations.py"},
		{"schemas path glob", "schemas/user.generated.json", true, "schemas/*.generated.json"},
		{"non-match", "regular.go", false, ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			out, err := ext.OnEvent(context.Background(), fileChangedEvent(t, tc.path))
			if err != nil {
				t.Fatalf("OnEvent: %v", err)
			}
			if !tc.wantMatch {
				if len(out) != 0 {
					t.Fatalf("expected zero events for %q, got %d", tc.path, len(out))
				}
				return
			}
			if len(out) != 1 {
				t.Fatalf("expected 1 event for %q, got %d", tc.path, len(out))
			}
			ev := out[0]
			if ev.Layer != "code.framework" {
				t.Errorf("Layer: got %q, want %q", ev.Layer, "code.framework")
			}
			if ev.Kind != "GeneratedArtifactAdded" {
				t.Errorf("Kind: got %q, want GeneratedArtifactAdded", ev.Kind)
			}
			var pl map[string]any
			if err := json.Unmarshal(ev.Payload, &pl); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			if gen, _ := pl["generator"].(string); gen != "manifest" {
				t.Errorf("generator: got %q, want manifest", gen)
			}
			if sent, _ := pl["sentinel"].(string); sent != "manifest:"+tc.wantGlob {
				t.Errorf("sentinel: got %q, want manifest:%s", sent, tc.wantGlob)
			}
			prov, _ := pl["provenance"].(map[string]any)
			if prov == nil {
				t.Fatalf("provenance missing")
			}
			if conf, _ := prov["confidence"].(float64); conf != 1.0 {
				t.Errorf("confidence: got %v, want 1.0", conf)
			}
		})
	}
}

func TestManifestExtractor_EmptyPatterns(t *testing.T) {
	ext := newTestExtractor(t, nil)
	out, err := ext.OnEvent(context.Background(), fileChangedEvent(t, "anything.go"))
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected zero events with empty patterns, got %d", len(out))
	}
}

func TestManifestExtractor_MissingPayload(t *testing.T) {
	ext := newTestExtractor(t, []string{"*.go"})
	out, err := ext.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged",
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected zero events for empty path, got %d", len(out))
	}
}

// TestManifestExtractor_RealConfig exercises the production loader by
// scaffolding a `.graph-harness/extractors.toml` in a temp workspace.
func TestManifestExtractor_RealConfig(t *testing.T) {
	ws := t.TempDir()
	cfgDir := filepath.Join(ws, ".graph-harness")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src, err := os.ReadFile(filepath.Join("testdata", "extractors.toml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "extractors.toml"), src, 0o644); err != nil {
		t.Fatalf("write extractors.toml: %v", err)
	}

	ext, err := New(cf.Deps{Workspace: ws, Logf: t.Logf})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	out, err := ext.OnEvent(context.Background(), fileChangedEvent(t, "i18n_translations.py"))
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 event from manifest-declared file, got %d", len(out))
	}
	if out[0].Kind != "GeneratedArtifactAdded" {
		t.Errorf("Kind: got %q, want GeneratedArtifactAdded", out[0].Kind)
	}
}

func TestManifestExtractor_Registered(t *testing.T) {
	ctor, desc, ok := cf.Lookup(Name)
	if !ok {
		t.Fatalf("extractor %q not registered", Name)
	}
	if ctor == nil {
		t.Fatalf("nil constructor")
	}
	if desc.Family != "generated" {
		t.Errorf("Family: got %q, want generated", desc.Family)
	}
	if len(desc.Outputs) != 1 || desc.Outputs[0] != cf.KindGeneratedArtifact {
		t.Errorf("Outputs: got %v, want [%s]", desc.Outputs, cf.KindGeneratedArtifact)
	}
}
