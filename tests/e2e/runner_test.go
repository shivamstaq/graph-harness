// Package e2e is the end-to-end test surface for the graph-harness CLI.
//
// Specs live under tests/e2e/specs/<wave>/...; this single TestE2E function
// builds the CLI binary and dispatches every spec as a parallel subtest via
// the gotit testdriver. See docs/gotit-integration.md for project-level
// rationale and tests/e2e/specs/boilerplate/ for Phase 0 coverage.
package e2e

import (
	"testing"

	"github.com/shivamstaq/gotit/runner"
	"github.com/shivamstaq/gotit/runner/testdriver"

	"github.com/shivamstaq/graph-harness/tests/e2e/helpers"
)

// TestE2E runs every YAML spec under tests/e2e/specs/. Filter via go test:
//
//	go test ./tests/e2e/ -run "TestE2E/boilerplate/cli/"
func TestE2E(t *testing.T) {
	testdriver.Run(t, runner.Config{
		BinaryName: "graph-harness",
		BuildPath:  "./cmd/graph-harness",
		EnvPrefix:  "GRAPH_HARNESS",
		RepoHelpers: map[string]runner.RepoHelper{
			"go-module-empty":                       helpers.GoModuleEmpty,
			"go-module-with-checkout-validator":     helpers.GoModuleWithCheckoutValidator,
			"ts-module-with-checkout-validator":     helpers.TSModuleWithCheckoutValidator,
			"python-module-with-checkout-validator": helpers.PythonModuleWithCheckoutValidator,
			"polyglot-repo-go-ts-py":                helpers.PolyglotRepoGoTSPy,
		},
		RequirementCheckers: map[string]runner.RequirementChecker{
			"cgo":             helpers.CheckCGO,
			"tree-sitter":     helpers.CheckTreeSitter,
			"gopls":           helpers.CheckGopls,
			"tsserver":        helpers.CheckTSServer,
			"pyright":         helpers.CheckPyright,
			"scip-go":         helpers.CheckSCIPGo,
			"scip-typescript": helpers.CheckSCIPTypeScript,
			"scip-python":     helpers.CheckSCIPPython,
		},
		Assertions: map[string]runner.AssertionFunc{
			"x-event-log-monotonic": helpers.AssertEventLogMonotonic,
			"x-finding-shape-valid": helpers.AssertFindingShapeValid,
			"x-manifest-valid":      helpers.AssertManifestValid,
		},
	})
}
