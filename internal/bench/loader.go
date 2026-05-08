package bench

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LanguageVariant is one language-specific instance of a bench scenario.
// Each variant directory under tests/testdata/bench/scenario<N>/ ships:
//
//	<lang>/oracle.json   — declares scenario_id, language, expected[]
//	<lang>/target.diff   — the unified diff the runner feeds to change.process
//	<lang>/.graph-harness/overlay/<file>.gh — the flow declarations
//	<lang>/<source files> — code that change.process resolves against
type LanguageVariant struct {
	// Language is the canonical extractor language ID (lowercased)
	// matching the directory name and the oracle's `language` field
	// after canonicalization. Values: "go", "typescript", "python".
	Language string
	// Path is the absolute path to the variant's root (i.e. the
	// directory containing oracle.json + target.diff).
	Path string
	// Diff is the bytes of target.diff verbatim, ready to feed to
	// change_process.Pipeline.ValidateDiff.
	Diff []byte
	// Oracle is the parsed oracle.json envelope.
	Oracle Oracle
}

// Oracle is the JSON shape declared by each variant's oracle.json.
type Oracle struct {
	ScenarioID int               `json:"scenario_id"`
	Language   string            `json:"language"`
	Name       string            `json:"name"`
	Expected   []ExpectedFinding `json:"expected"`
}

// canonicalLanguage normalizes an oracle's `language` field or a
// `--language` flag value to the directory name used under the scenario
// root. Accepts the common synonyms (typescript|ts, python|py, go|golang).
func canonicalLanguage(in string) string {
	switch strings.ToLower(strings.TrimSpace(in)) {
	case "go", "golang":
		return "go"
	case "ts", "typescript", "tsx", "javascript", "js", "jsx":
		return "ts"
	case "py", "python":
		return "py"
	}
	return ""
}

// LoadScenario reads a bench scenario from disk. scenarioRoot points at
// tests/testdata/bench/scenario<N>/ (the directory containing per-language
// subdirectories like go/, ts/, py/). languages is the requested filter:
// pass nil or {"all"} to load every variant the directory exposes.
//
// Returns the scenario meta plus the loaded variants in canonical-language
// order (alphabetical by canonical id) so two equivalent runs produce
// byte-identical JSON output.
//
// Errors:
//
//   - the scenarioRoot does not exist;
//   - no language variants are found inside it;
//   - the requested language filter matches nothing on disk;
//   - any per-variant file (oracle.json, target.diff) is missing.
func LoadScenario(scenarioRoot string, scenarioID int, languages []string) (Scenario, []LanguageVariant, error) {
	scenarioRoot, err := filepath.Abs(scenarioRoot)
	if err != nil {
		return Scenario{}, nil, fmt.Errorf("resolve scenario root: %w", err)
	}
	info, err := os.Stat(scenarioRoot)
	if err != nil {
		return Scenario{}, nil, fmt.Errorf("scenario root: %w", err)
	}
	if !info.IsDir() {
		return Scenario{}, nil, fmt.Errorf("scenario root %s is not a directory", scenarioRoot)
	}

	wantAll := len(languages) == 0
	wantSet := map[string]struct{}{}
	for _, lang := range languages {
		c := canonicalLanguage(lang)
		if lang == "all" || lang == "" {
			wantAll = true
			continue
		}
		if c == "" {
			return Scenario{}, nil, fmt.Errorf("unknown language %q (want one of go|typescript|python|all)", lang)
		}
		wantSet[c] = struct{}{}
	}

	entries, err := os.ReadDir(scenarioRoot)
	if err != nil {
		return Scenario{}, nil, fmt.Errorf("read scenario root: %w", err)
	}

	variants := make([]LanguageVariant, 0, 3)
	scenarioName := ""
	scenarioLanguages := make([]string, 0, 3)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		canonical := canonicalLanguage(e.Name())
		if canonical == "" {
			continue
		}
		if !wantAll {
			if _, ok := wantSet[canonical]; !ok {
				continue
			}
		}
		variantPath := filepath.Join(scenarioRoot, e.Name())
		oraclePath := filepath.Join(variantPath, "oracle.json")
		oracleBytes, err := os.ReadFile(oraclePath) // #nosec G304 -- variantPath rooted under operator-supplied scenarioRoot
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// Directory matches a language name but isn't actually a
				// variant (e.g. a stray `js/` directory with build output).
				continue
			}
			return Scenario{}, nil, fmt.Errorf("read %s: %w", oraclePath, err)
		}
		var oracle Oracle
		if err := json.Unmarshal(oracleBytes, &oracle); err != nil {
			return Scenario{}, nil, fmt.Errorf("parse %s: %w", oraclePath, err)
		}
		if oracle.ScenarioID != scenarioID {
			// Allow a single root to hold multiple scenarios eventually;
			// today we have just scenario 1, and a mismatch means the
			// caller pointed at the wrong directory.
			return Scenario{}, nil, fmt.Errorf("oracle %s declares scenario_id=%d, want %d", oraclePath, oracle.ScenarioID, scenarioID)
		}
		diffPath := filepath.Join(variantPath, "target.diff")
		diff, err := os.ReadFile(diffPath) // #nosec G304 -- variantPath rooted under operator-supplied scenarioRoot
		if err != nil {
			return Scenario{}, nil, fmt.Errorf("read %s: %w", diffPath, err)
		}
		variants = append(variants, LanguageVariant{
			Language: canonical,
			Path:     variantPath,
			Diff:     diff,
			Oracle:   oracle,
		})
		// First non-empty oracle name wins as the scenario-level name.
		if scenarioName == "" {
			scenarioName = strings.TrimSuffix(oracle.Name, " ("+oracle.Language+")")
		}
		scenarioLanguages = append(scenarioLanguages, oracle.Language)
	}

	if len(variants) == 0 {
		return Scenario{}, nil, fmt.Errorf("no language variants found under %s (looked for go/, ts/, py/)", scenarioRoot)
	}

	sort.Slice(variants, func(i, j int) bool { return variants[i].Language < variants[j].Language })
	sort.Strings(scenarioLanguages)

	scenario := Scenario{
		ID:          scenarioID,
		Name:        scenarioName,
		Description: fmt.Sprintf("scenario %d auth-sensitive edit (cross-language)", scenarioID),
		Languages:   scenarioLanguages,
	}
	return scenario, variants, nil
}

// DiscoverScenarioRoot walks up from cwd looking for the canonical
// tests/testdata/bench/scenario<N>/ tree. Returns the absolute path or an
// error if no such tree is found above cwd.
func DiscoverScenarioRoot(cwd string, scenarioID int) (string, error) {
	target := filepath.Join("tests", "testdata", "bench", fmt.Sprintf("scenario%d", scenarioID))
	dir := cwd
	for {
		candidate := filepath.Join(dir, target)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return filepath.Abs(candidate)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("could not discover %s above %s", target, cwd)
}
