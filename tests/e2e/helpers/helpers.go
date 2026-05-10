// Package helpers provides graph-harness-specific gotit registrations:
// repo helpers, requirement checkers, and custom assertion types.
//
// Helpers stay small and exemplary — they are the authoritative builders
// of E2E fixtures. Anything project-specific belongs here, not in the
// gotit kit.
package helpers

import (
	"errors"
	"os/exec"

	"github.com/shivamstaq/gotit/runner"
)

// CheckCGO verifies that a C toolchain is available — required for tree-sitter.
func CheckCGO(_ string) error {
	if _, err := exec.LookPath("cc"); err != nil {
		if _, err2 := exec.LookPath("gcc"); err2 != nil {
			return errors.New("no C compiler on PATH (need cc or gcc for cgo)")
		}
	}
	return nil
}

// CheckTreeSitter verifies tree-sitter cgo bindings link by checking that
// the just-built graph-harness binary can spawn a parse via the smoke command.
// (Until the smoke command exists, we proxy via cgo availability.)
func CheckTreeSitter(_ string) error {
	return CheckCGO("")
}

// checkOnPath is the generic shape for binary-presence requirement
// checkers. The phase1 wave wires gopls / tsserver / pyright / scip-go /
// scip-typescript / scip-python through this — each spec that needs the
// real fact source declares its `requires:` entry, and gotit skips the
// spec when the binary is missing rather than failing it.
func checkOnPath(name, hint string) func(string) error {
	return func(_ string) error {
		if _, err := exec.LookPath(name); err != nil {
			if hint == "" {
				return errors.New(name + " not found on PATH")
			}
			return errors.New(name + " not found on PATH (" + hint + ")")
		}
		return nil
	}
}

// CheckGopls verifies gopls is on PATH. Required by Go LSP-fact specs.
func CheckGopls(_ string) error {
	return checkOnPath("gopls", "go install golang.org/x/tools/gopls@latest")("")
}

// CheckTSServer verifies typescript-language-server is on PATH.
func CheckTSServer(_ string) error {
	return checkOnPath("typescript-language-server", "npm i -g typescript-language-server typescript")("")
}

// CheckPyright verifies pyright is on PATH.
func CheckPyright(_ string) error {
	return checkOnPath("pyright", "npm i -g pyright")("")
}

// CheckSCIPGo verifies scip-go is on PATH.
func CheckSCIPGo(_ string) error {
	return checkOnPath("scip-go", "go install github.com/scip-code/scip-go/cmd/scip-go@latest")("")
}

// CheckSCIPTypeScript verifies scip-typescript is on PATH.
func CheckSCIPTypeScript(_ string) error {
	return checkOnPath("scip-typescript", "npm i -g @sourcegraph/scip-typescript")("")
}

// CheckSCIPPython verifies scip-python is on PATH.
func CheckSCIPPython(_ string) error {
	return checkOnPath("scip-python", "npm i -g @sourcegraph/scip-python")("")
}

// Compile-time assertion that we satisfy the runner interfaces.
var (
	_ runner.RepoHelper         = GoModuleEmpty
	_ runner.RepoHelper         = GoModuleWithCheckoutValidator
	_ runner.RepoHelper         = TSModuleWithCheckoutValidator
	_ runner.RepoHelper         = PythonModuleWithCheckoutValidator
	_ runner.RepoHelper         = PolyglotRepoGoTSPy
	_ runner.RequirementChecker = CheckCGO
	_ runner.RequirementChecker = CheckTreeSitter
	_ runner.RequirementChecker = CheckGopls
	_ runner.RequirementChecker = CheckTSServer
	_ runner.RequirementChecker = CheckPyright
	_ runner.RequirementChecker = CheckSCIPGo
	_ runner.RequirementChecker = CheckSCIPTypeScript
	_ runner.RequirementChecker = CheckSCIPPython
)
