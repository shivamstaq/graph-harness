package bench

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/shivamstaq/graph-harness/internal/change_process"
	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/semantic_overlay"
)

// scenario2RepoRoot returns the absolute path to the scenario-2 fixture
// shipped under tests/testdata/bench/scenario2/. Walks up from this
// file's location so the test is location-independent (no reliance on
// a particular cwd at test time).
func scenario2RepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller(0) failed")
	}
	// internal/bench/scenario2_test.go -> walk up to repo root, then
	// into tests/testdata/bench/scenario2/.
	dir := filepath.Dir(thisFile) // .../internal/bench
	for i := 0; i < 6; i++ {
		candidate := filepath.Join(dir, "tests", "testdata", "bench", "scenario2")
		if info, err := abs(candidate); err == nil && info != "" {
			return info
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate tests/testdata/bench/scenario2/ from %s", thisFile)
	return ""
}

// abs returns the absolute path if it exists as a directory, or ""
// otherwise. Returns a non-nil error only for path-resolution failures
// (a missing directory yields "", nil).
func abs(path string) (string, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", nil //nolint:nilerr // missing dir is "not found", caller iterates
	}
	if info.IsDir() {
		return p, nil
	}
	return "", nil
}

// newScenario2Pipeline builds the in-memory pipeline + store + overlay
// the test harness uses. Mirrors newTestPipeline in pipeline_test.go
// but lives under internal/bench so the bench package's seed
// application path is exercised end-to-end.
func newScenario2Pipeline(t *testing.T) (*change_process.Pipeline, *code_core.Store) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.db")
	log, err := facts.OpenEventLog(logPath)
	if err != nil {
		t.Fatalf("OpenEventLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := code_core.NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	overlay := semantic_overlay.NewOverlay()
	resolver, err := semantic_overlay.NewResolver(overlay, store, nil)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	return &change_process.Pipeline{
		Overlay:  overlay,
		Code:     store,
		Resolver: resolver,
		Events:   log,
	}, store
}

// TestBenchScenario2_LoadFixture exercises the fixture loader: oracle
// + seed + three target diffs must all be present + parseable.
func TestBenchScenario2_LoadFixture(t *testing.T) {
	root := scenario2RepoRoot(t)
	fx, err := LoadScenario2(root)
	if err != nil {
		t.Fatalf("LoadScenario2: %v", err)
	}
	if fx.Oracle.ScenarioID != 2 {
		t.Errorf("oracle.scenario_id = %d, want 2", fx.Oracle.ScenarioID)
	}
	if got, want := len(fx.Oracle.Diffs), 3; got != want {
		t.Errorf("oracle.diffs count = %d, want %d", got, want)
	}
	for _, d := range fx.Oracle.Diffs {
		if len(fx.DiffBytes[d.DiffID]) == 0 {
			t.Errorf("diff %q bytes empty", d.DiffID)
		}
		if len(d.Expected) == 0 {
			t.Errorf("diff %q has no expected findings", d.DiffID)
		}
	}
	if len(fx.Seed.Entities) == 0 {
		t.Errorf("seed.entities empty")
	}
	if len(fx.Seed.SelectorBindings) == 0 {
		t.Errorf("seed.selector_bindings empty")
	}
	// Every dependent in the oracle MUST be present in the seed as
	// an entity (otherwise the pipeline would have no row to surface).
	seedIDs := map[string]struct{}{}
	for _, e := range fx.Seed.Entities {
		seedIDs[e.ID] = struct{}{}
	}
	for _, d := range fx.Oracle.Diffs {
		for _, f := range d.Expected {
			for _, dep := range f.Dependents {
				if _, ok := seedIDs[dep.EntityID]; !ok {
					t.Errorf("diff %q expects dependent %q but seed has no such entity",
						d.DiffID, dep.EntityID)
				}
			}
		}
	}
}

// TestBenchScenario2_DetectionAxis is the main gate: load the fixture,
// apply the seed, run validate-diff against every target diff, and
// assert min(per-language detection-axis score) ≥ Scenario2Gate.
// Sub-tests per (diff × language) so a regression in a single
// dependent language is localized in the failure output.
func TestBenchScenario2_DetectionAxis(t *testing.T) {
	root := scenario2RepoRoot(t)
	fx, err := LoadScenario2(root)
	if err != nil {
		t.Fatalf("LoadScenario2: %v", err)
	}

	pipeline, store := newScenario2Pipeline(t)
	runner := &Scenario2Runner{
		Pipeline:      pipeline,
		Store:         store,
		ValidationSeq: 1,
	}
	if err := runner.ApplySeed(context.Background(), fx.Seed); err != nil {
		t.Fatalf("ApplySeed: %v", err)
	}
	result, err := runner.Run(context.Background(), fx)
	if err != nil {
		t.Fatalf("runner.Run: %v", err)
	}

	// Scenario-level gate first.
	if !result.PassesGate {
		t.Errorf("scenario detection axis below %0.2f gate: %+v", Scenario2Gate, result.DetectionAxis)
	}
	for lang, cell := range result.DetectionAxis {
		if cell.Score < Scenario2Gate {
			t.Errorf("language %q score=%.2f < %0.2f gate (matched %d / expected %d)",
				lang, cell.Score, Scenario2Gate, cell.Matched, cell.Expected)
		}
	}

	// Per-(diff × language) sub-tests so failures localize to a
	// specific dependent language inside a specific diff.
	for _, dr := range result.PerDiff {
		dr := dr
		t.Run(dr.DiffID, func(t *testing.T) {
			if dr.FindingsObserved == 0 {
				t.Errorf("zero missing_dependent_update findings observed for diff %q", dr.DiffID)
			}
			for lang, cell := range dr.DetectionAxis {
				lang, cell := lang, cell
				t.Run(lang, func(t *testing.T) {
					if cell.Score < Scenario2Gate {
						t.Errorf("diff %q language %q score=%.2f < %0.2f (matched %d / expected %d); missed=%v",
							dr.DiffID, lang, cell.Score, Scenario2Gate,
							cell.Matched, cell.Expected, dr.MissedDependents)
					}
				})
			}
		})
	}
}

// TestBenchScenario2_RunnerNoSeedFails confirms the runner returns the
// sentinel error when handed an empty seed (defensive — a fixture
// authoring mistake should fail loud).
func TestBenchScenario2_RunnerNoSeedFails(t *testing.T) {
	pipeline, store := newScenario2Pipeline(t)
	runner := &Scenario2Runner{Pipeline: pipeline, Store: store}
	err := runner.ApplySeed(context.Background(), Scenario2Seed{})
	if err == nil {
		t.Fatalf("expected ErrScenario2NoSeed, got nil")
	}
}

// TestBenchScenario2_LanguageFold asserts the Scenario2Languages
// helper produces a deterministic sorted dedup of every language
// referenced across every diff's expected dependents.
func TestBenchScenario2_LanguageFold(t *testing.T) {
	o := Scenario2Oracle{
		Diffs: []Scenario2DiffSpec{
			{Expected: []Scenario2FindingExpect{
				{Dependents: []Scenario2DependentExpect{
					{Language: "typescript"}, {Language: "python"},
				}},
			}},
			{Expected: []Scenario2FindingExpect{
				{Dependents: []Scenario2DependentExpect{
					{Language: "go"}, {Language: "typescript"},
				}},
			}},
		},
	}
	got := o.Scenario2Languages()
	want := []string{"go", "python", "typescript"}
	if len(got) != len(want) {
		t.Fatalf("Scenario2Languages = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("Scenario2Languages[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
