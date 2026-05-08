package bench

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile is a small helper that mkdir's and writes a file in one shot.
// Tests use it to lay out scenario fixtures inline.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// stageVariant writes the canonical files a LanguageVariant needs:
// oracle.json + target.diff. Both are valid JSON / unified-diff bytes
// the loader will accept; tests can override the oracle body via the
// `oracle` argument.
func stageVariant(t *testing.T, root, lang, oracle string) {
	t.Helper()
	writeFile(t, filepath.Join(root, lang, "oracle.json"), oracle)
	writeFile(t, filepath.Join(root, lang, "target.diff"), "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n")
}

func TestLoadScenario_AllLanguagesDefault(t *testing.T) {
	root := t.TempDir()
	stageVariant(t, root, "go", `{"scenario_id":1,"language":"go","name":"go-name","expected":[{"kind":"flow_unreviewed","flow":"F"}]}`)
	stageVariant(t, root, "ts", `{"scenario_id":1,"language":"typescript","name":"ts-name","expected":[{"kind":"flow_unreviewed","flow":"F"}]}`)
	stageVariant(t, root, "py", `{"scenario_id":1,"language":"python","name":"py-name","expected":[{"kind":"flow_unreviewed","flow":"F"}]}`)

	scenario, variants, err := LoadScenario(root, 1, nil)
	if err != nil {
		t.Fatalf("LoadScenario: %v", err)
	}
	if got, want := len(variants), 3; got != want {
		t.Fatalf("variant count = %d, want %d", got, want)
	}
	// Canonical-language order: go < py < ts.
	for i, want := range []string{"go", "py", "ts"} {
		if variants[i].Language != want {
			t.Errorf("variant[%d].Language = %q, want %q", i, variants[i].Language, want)
		}
		if len(variants[i].Diff) == 0 {
			t.Errorf("variant[%d] diff empty", i)
		}
	}
	if got, want := len(scenario.Languages), 3; got != want {
		t.Errorf("scenario.Languages = %d, want %d (%v)", got, want, scenario.Languages)
	}
}

func TestLoadScenario_FilterSingleLanguage(t *testing.T) {
	root := t.TempDir()
	stageVariant(t, root, "go", `{"scenario_id":1,"language":"go","name":"go-name","expected":[]}`)
	stageVariant(t, root, "ts", `{"scenario_id":1,"language":"typescript","name":"ts-name","expected":[]}`)

	_, variants, err := LoadScenario(root, 1, []string{"typescript"})
	if err != nil {
		t.Fatalf("LoadScenario: %v", err)
	}
	if len(variants) != 1 || variants[0].Language != "ts" {
		t.Fatalf("filter=typescript matched %v, want [ts]", variants)
	}
}

func TestLoadScenario_FilterRejectsUnknownLanguage(t *testing.T) {
	root := t.TempDir()
	stageVariant(t, root, "go", `{"scenario_id":1,"language":"go","name":"x","expected":[]}`)
	if _, _, err := LoadScenario(root, 1, []string{"rust"}); err == nil {
		t.Fatalf("expected error for unknown language")
	}
}

func TestLoadScenario_RejectsScenarioIDMismatch(t *testing.T) {
	root := t.TempDir()
	stageVariant(t, root, "go", `{"scenario_id":7,"language":"go","name":"x","expected":[]}`)
	if _, _, err := LoadScenario(root, 1, nil); err == nil {
		t.Fatalf("expected error for oracle.scenario_id != requested id")
	}
}

func TestLoadScenario_RequiresAtLeastOneVariant(t *testing.T) {
	root := t.TempDir()
	if _, _, err := LoadScenario(root, 1, nil); err == nil {
		t.Fatalf("expected error for empty scenario directory")
	}
}

func TestLoadScenario_IgnoresUnknownDirs(t *testing.T) {
	root := t.TempDir()
	// build/ and dist/ aren't language directories.
	if err := os.MkdirAll(filepath.Join(root, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	stageVariant(t, root, "go", `{"scenario_id":1,"language":"go","name":"x","expected":[]}`)

	_, variants, err := LoadScenario(root, 1, nil)
	if err != nil {
		t.Fatalf("LoadScenario: %v", err)
	}
	if len(variants) != 1 {
		t.Fatalf("expected 1 variant, got %d", len(variants))
	}
}

func TestDiscoverScenarioRoot_FindsFromNestedCwd(t *testing.T) {
	root := t.TempDir()
	scenario := filepath.Join(root, "tests", "testdata", "bench", "scenario1")
	if err := os.MkdirAll(scenario, 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "internal", "cli")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverScenarioRoot(deep, 1)
	if err != nil {
		t.Fatalf("DiscoverScenarioRoot: %v", err)
	}
	wantAbs, _ := filepath.Abs(scenario)
	if got != wantAbs {
		t.Errorf("got %q, want %q", got, wantAbs)
	}
}

func TestCanonicalLanguage_SynonymTable(t *testing.T) {
	cases := map[string]string{
		"go": "go", "Go": "go", "golang": "go",
		"ts": "ts", "typescript": "ts", "TSX": "ts", "javascript": "ts", "js": "ts", "jsx": "ts",
		"py": "py", "python": "py", "PYTHON": "py",
		"rust": "", "": "",
	}
	for in, want := range cases {
		if got := canonicalLanguage(in); got != want {
			t.Errorf("canonicalLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}
