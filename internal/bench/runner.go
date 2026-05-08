// Package bench implements the bench.scenarios + bench.oracle layers and the
// `graph-harness bench` runner. P0.T46-T48: scenario 1 only (auth-sensitive
// edit, Go), mature regime, detection axis only.
package bench

import (
	"encoding/json"

	"github.com/shivamstaq/graph-harness/internal/change_process"
)

// Scenario describes one bench fixture (SPEC §12).
type Scenario struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	Regime      string   `json:"regime"`
	Description string   `json:"description"`
	Languages   []string `json:"languages"`
}

// ExpectedFinding is the oracle ground-truth shape (SPEC §12).
type ExpectedFinding struct {
	Kind         string `json:"kind"`
	Severity     string `json:"severity"`
	SubjectQName string `json:"subject_qualified_name"`
	Flow         string `json:"flow"`
}

// AxisScore tracks one of the four axes from SPEC §12.
type AxisScore struct {
	Score    float64 `json:"score"`
	Notes    string  `json:"notes,omitempty"`
	Detected int     `json:"detected,omitempty"`
	Expected int     `json:"expected,omitempty"`
}

// Result is the per-scenario score envelope.
type Result struct {
	Scenario        Scenario  `json:"scenario"`
	DetectionAxis   AxisScore `json:"detection_axis"`
	RepairQuality   AxisScore `json:"repair_quality"`
	RepairExecution AxisScore `json:"repair_execution"`
	FinalCleanPatch AxisScore `json:"final_clean_patch"`
	Findings        []string  `json:"findings_observed"`
	Expected        []string  `json:"findings_expected"`
}

// Score compares a pipeline result against the oracle.
func Score(scenario Scenario, observed *change_process.ValidateDiffResult, expected []ExpectedFinding) *Result {
	res := &Result{
		Scenario: scenario,
		DetectionAxis: AxisScore{
			Score:    0,
			Expected: len(expected),
		},
		RepairQuality:   AxisScore{Score: -1, Notes: "N/A in P0 (deferred to P3)"},
		RepairExecution: AxisScore{Score: -1, Notes: "N/A in P0 (deferred to P3)"},
		FinalCleanPatch: AxisScore{Score: -1, Notes: "N/A in P0 (deferred to P3)"},
	}
	matched := 0
	for _, want := range expected {
		for _, got := range observed.Findings {
			if got.Kind == want.Kind && got.Subject.Flow == want.Flow {
				matched++
				break
			}
		}
	}
	res.DetectionAxis.Detected = matched
	if len(expected) > 0 {
		res.DetectionAxis.Score = float64(matched) / float64(len(expected))
	} else if len(observed.Findings) == 0 {
		res.DetectionAxis.Score = 1.0
	}
	for _, f := range observed.Findings {
		res.Findings = append(res.Findings, f.Kind+":"+f.Subject.Flow)
	}
	for _, e := range expected {
		res.Expected = append(res.Expected, e.Kind+":"+e.Flow)
	}
	return res
}

// AsJSON renders a Result as pretty JSON for CLI output.
func (r *Result) AsJSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// MultiResult is the envelope the polyglot bench runner emits when more
// than one language variant of a scenario was scored. It carries the
// per-language Result list plus an aggregate detection-axis fold so a
// single `--language all` invocation produces a JSON document that
// answers plan §3 gate criterion 10 ("oracle pass rates ≥ 0.8 per
// language") in one shape.
type MultiResult struct {
	Scenario    Scenario           `json:"scenario"`
	PerLanguage map[string]*Result `json:"per_language"`
	Aggregate   AxisScore          `json:"aggregate"`
}

// Aggregate folds per-language detection-axis scores into a single axis
// score: detection_axis = sum(detected) / sum(expected) over every
// language. Repair / final-clean axes are propagated as `N/A` until P3.
//
// The aggregate is computed in canonical-language order so two equivalent
// runs produce byte-identical JSON.
func Aggregate(scenario Scenario, perLang map[string]*Result) MultiResult {
	out := MultiResult{
		Scenario:    scenario,
		PerLanguage: perLang,
	}
	totalDetected := 0
	totalExpected := 0
	for _, r := range perLang {
		totalDetected += r.DetectionAxis.Detected
		totalExpected += r.DetectionAxis.Expected
	}
	axis := AxisScore{Detected: totalDetected, Expected: totalExpected}
	if totalExpected > 0 {
		axis.Score = float64(totalDetected) / float64(totalExpected)
	} else if totalExpected == 0 {
		// All language oracles declared zero expected findings; only
		// counts as a pass if no findings fired across any language.
		anyFiring := false
		for _, r := range perLang {
			if len(r.Findings) > 0 {
				anyFiring = true
				break
			}
		}
		if !anyFiring {
			axis.Score = 1.0
		}
	}
	out.Aggregate = axis
	return out
}

// AsJSON renders a MultiResult as pretty JSON for CLI output.
func (m MultiResult) AsJSON() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}
