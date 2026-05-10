package helpers

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/shivamstaq/gotit/runner"
)

// AssertEventLogMonotonic reads the workspace's SQLite event log and asserts
// that kernel_events.seq is strictly monotonically increasing across all rows.
//
// YAML usage:
//
//   - type: x-event-log-monotonic
//     path: ".graph-harness/state.db"   # relative to workDir; default if empty
//
// The runner's <PREFIX>_E2E_PROJECT_ROOT env var is the consumer's repo root,
// not the spec's workDir, so callers pass the workspace-relative path here.
// We detect workDir from the runner's exec environment via stdout convention:
// the first call reads the path from result.Stdout if path is empty.
func AssertEventLogMonotonic(a runner.Assertion, r runner.StepResult, _ map[string]string) runner.AssertionResult {
	const summary = "kernel_events.seq strictly monotonic"
	const want = "strictly increasing seq across rows"

	dbPath := a.Path
	if dbPath == "" {
		// Convention: a preceding step prints the absolute path of the event log
		// to stdout (e.g. via `graph-harness daemon db-path`).
		dbPath = filepath.Clean(r.Stdout)
	}
	if dbPath == "" {
		return assertionFailed(summary, want, "(no path)",
			fmt.Errorf("path is empty and stdout did not carry a path"))
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return assertionFailed(summary, want, "sqlite3 not on PATH",
			fmt.Errorf("sqlite3 CLI not on PATH (install or use a Go-side helper)"))
	}
	// #nosec G204 -- dbPath is sourced from the spec/test fixture, not user input.
	out, err := exec.Command("sqlite3", dbPath,
		"SELECT seq FROM kernel_events ORDER BY ROWID").CombinedOutput()
	if err != nil {
		return assertionFailed(summary, want, string(out),
			fmt.Errorf("sqlite3 query failed: %v", err))
	}
	var prev int64 = -1
	for _, line := range splitLines(string(out)) {
		if line == "" {
			continue
		}
		var seq int64
		if _, err := fmt.Sscanf(line, "%d", &seq); err != nil {
			return assertionFailed(summary, want, line,
				fmt.Errorf("bad seq row %q: %v", line, err))
		}
		if seq <= prev {
			return assertionFailed(summary, want,
				fmt.Sprintf("seq %d after %d", seq, prev),
				fmt.Errorf("non-monotonic at seq %d (prev %d)", seq, prev))
		}
		prev = seq
	}
	return assertionPassed(summary, want, fmt.Sprintf("last seq %d", prev))
}

// AssertFindingShapeValid validates a JSON ValidationFinding (or array of them)
// against the SPEC §8.2 shape. The finding text comes from r.Stdout.
//
//   - type: x-finding-shape-valid
//     path: "$[0]"   # optional JSONPath; default = whole stdout
func AssertFindingShapeValid(a runner.Assertion, r runner.StepResult, _ map[string]string) runner.AssertionResult {
	const summary = "finding has SPEC §8.2 fields"
	const want = "id+kind+severity+subject+evidence+repair present"

	var raw any
	if err := json.Unmarshal([]byte(r.Stdout), &raw); err != nil {
		return assertionFailed(summary, want, truncate(r.Stdout, 80),
			fmt.Errorf("stdout is not valid JSON: %v", err))
	}
	target := raw
	if a.Path != "" {
		var idx int
		if _, err := fmt.Sscanf(a.Path, "$[%d]", &idx); err != nil {
			return assertionFailed(summary, want, a.Path,
				fmt.Errorf("only $[N] paths supported, got %q", a.Path))
		}
		arr, ok := raw.([]any)
		if !ok {
			return assertionFailed(summary, want, "stdout not an array", fmt.Errorf("stdout is not an array"))
		}
		if idx >= len(arr) {
			return assertionFailed(summary, want,
				fmt.Sprintf("len=%d, idx=%d", len(arr), idx),
				fmt.Errorf("index %d out of range (len %d)", idx, len(arr)))
		}
		target = arr[idx]
	}
	finding, ok := target.(map[string]any)
	if !ok {
		return assertionFailed(summary, want, fmt.Sprintf("%T", target),
			fmt.Errorf("target is not a JSON object"))
	}
	required := []string{"id", "kind", "severity", "subject", "evidence"}
	for _, k := range required {
		if _, ok := finding[k]; !ok {
			return assertionFailed(summary, want, "missing "+k,
				fmt.Errorf("missing required field %q (SPEC §8.2)", k))
		}
	}
	if _, ok := finding["repair"]; !ok {
		return assertionFailed(summary, want, "missing repair",
			fmt.Errorf("missing repair field (may be empty in P0, but field must exist)"))
	}
	return assertionPassed(summary, want, "all required fields present")
}

// AssertManifestValid pipes a YAML manifest through `graph-harness layers
// install --dry-run` and asserts a clean exit. Used to confirm a generated
// manifest passes the validator without committing it.
//
//   - type: x-manifest-valid
//     path: ".graph-harness/manifests/foo.yaml"   # path relative to workDir
func AssertManifestValid(a runner.Assertion, _ runner.StepResult, _ map[string]string) runner.AssertionResult {
	summary := "manifest validates via layers install --dry-run"
	want := "exit 0 from validator"

	if a.Path == "" {
		return assertionFailed(summary, want, "(no path)", fmt.Errorf("path is required"))
	}
	// #nosec G204 -- a.Path is sourced from the spec, an authored test artifact.
	cmd := exec.Command("graph-harness", "layers", "install", "--dry-run", a.Path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return assertionFailed(summary, want, truncate(string(out), 200),
			fmt.Errorf("validator rejected %s: %v", a.Path, err))
	}
	return assertionPassed(summary, want, "validator accepted "+a.Path)
}

// assertionPassed / assertionFailed are local helpers mirroring gotit's
// internal passed/failed shape so each custom assertion populates Summary,
// Want, Got, and Error consistently with the v0.2.0 reporting model.
func assertionPassed(summary, want, got string) runner.AssertionResult {
	return runner.AssertionResult{Passed: true, Summary: summary, Want: want, Got: got}
}

func assertionFailed(summary, want, got string, err error) runner.AssertionResult {
	r := runner.AssertionResult{Passed: false, Summary: summary, Want: want, Got: got}
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
