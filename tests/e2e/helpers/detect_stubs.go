package helpers

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/shivamstaq/gotit/runner"
)

// PolyglotWithDetectStubs stages a polyglot Go/TS/Py workspace plus
// hermetic shell-script "stubs" at the workspace-local install
// locations the detector probes. The stubs print a recognisable
// version string when invoked with --version so detect.versionFromCommand
// can populate the report's Version field, but they do not implement
// LSP or SCIP — these specs only exercise detection, not extraction.
//
// Stubs staged:
//
//	./node_modules/.bin/typescript-language-server (TS LSP)
//	./node_modules/.bin/scip-typescript            (TS SCIP)
//	./.venv/bin/pyright-langserver                 (Py LSP)
//	./.venv/bin/scip-python                        (Py SCIP)
//	./vendor/bin/gopls                             (Go LSP)
//	./vendor/bin/scip-go                           (Go SCIP)
//
// Plus matching package.json + .venv marker so the detector's
// project-aware install hints + venv resolution see realistic context.
//
// Used by tests/e2e/specs/doctor/precedence-*.yaml.
func PolyglotWithDetectStubs(_, workDir string, _ map[string]any) error {
	if err := PolyglotRepoGoTSPy("", workDir, nil); err != nil {
		return err
	}
	stubs := []struct {
		path    string
		version string
	}{
		{filepath.Join("node_modules", ".bin", "typescript-language-server"), "stub-tsserver 99.0.0"},
		{filepath.Join("node_modules", ".bin", "scip-typescript"), "stub-scip-typescript 99.0.0"},
		{filepath.Join(".venv", "bin", "pyright-langserver"), "stub-pyright 99.0.0"},
		{filepath.Join(".venv", "bin", "scip-python"), "stub-scip-python 99.0.0"},
		{filepath.Join("vendor", "bin", "gopls"), "stub-gopls v99.0.0"},
		{filepath.Join("vendor", "bin", "scip-go"), "stub-scip-go 99.0.0"},
	}
	for _, s := range stubs {
		full := filepath.Join(workDir, s.path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return err
		}
		// Tiny POSIX shell script: print the version on --version, no-op otherwise.
		// On Windows the detector still finds the file (we don't test Windows in CI).
		body := "#!/bin/sh\nif [ \"$1\" = \"--version\" ] || [ \"$1\" = \"version\" ]; then echo '" + s.version + "'; exit 0; fi\nexit 0\n"
		if err := runner.WriteFile(full, body); err != nil {
			return err
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(full, 0o755); err != nil {
				return err
			}
		}
	}
	// Marker files so the detector's project-aware install hints + venv
	// resolution have a realistic context to react to.
	if err := runner.WriteFile(
		filepath.Join(workDir, "package.json"),
		`{"name":"detect-stub-fixture","version":"0.0.0","private":true}`,
	); err != nil {
		return err
	}
	if err := runner.WriteFile(filepath.Join(workDir, "pnpm-lock.yaml"), "lockfileVersion: 9.0\n"); err != nil {
		return err
	}
	return nil
}

// PolyglotMissingExtractors stages a polyglot workspace with NO
// extractor binaries on disk and a sanitised PATH (set per-spec via
// env:) so detection comes back missing across the board. We stage
// pnpm-lock.yaml + .venv/ + package.json so the detector's
// project-aware install-hint logic produces predictable output (pnpm
// for TS, pipx for pyright). Used by strict-extractors-exits.yaml
// and print-install-prefers-ecosystem.yaml.
func PolyglotMissingExtractors(_, workDir string, _ map[string]any) error {
	if err := PolyglotRepoGoTSPy("", workDir, nil); err != nil {
		return err
	}
	if err := runner.WriteFile(
		filepath.Join(workDir, "package.json"),
		`{"name":"detect-fixture","version":"0.0.0","private":true}`,
	); err != nil {
		return err
	}
	if err := runner.WriteFile(filepath.Join(workDir, "pnpm-lock.yaml"), "lockfileVersion: 9.0\n"); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(workDir, ".venv"), 0o750); err != nil {
		return err
	}
	return nil
}

// PythonWithBasedpyrightOverride stages a Python workspace with a
// .graph-harness/config.toml `[lsp.python] server = "basedpyright"`
// override. Used by config-override-resolves-basedpyright.yaml to
// verify the workspace config is honored.
func PythonWithBasedpyrightOverride(probe, workDir string, params map[string]any) error {
	if err := PythonModuleWithCheckoutValidator(probe, workDir, params); err != nil {
		return err
	}
	cfgDir := filepath.Join(workDir, ".graph-harness")
	if err := os.MkdirAll(cfgDir, 0o750); err != nil {
		return err
	}
	body := `[lsp.python]
server = "basedpyright"
`
	return runner.WriteFile(filepath.Join(cfgDir, "config.toml"), body)
}
