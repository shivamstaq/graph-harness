package detect

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

// pyDetector probes the Python ecosystem: pyright (LSP) and scip-python
// (SCIP). Tree-sitter-python is cgo-bundled.
//
// Probe order per SPEC §6.18:
//   1. workspace ./.venv/bin/<tool>                   (project_local)
//   2. $VIRTUAL_ENV/bin/<tool>                        (project_local)
//   3. pipx-managed venv (`pipx list --json`)         (ecosystem_user)
//   4. uv-managed tool venv (`uv tool list`)          (ecosystem_user)
//   5. ~/.local/bin/<tool>                            (ecosystem_user)
//   6. $PATH                                          (path)
//
// Pyright's npm package installs `pyright` and `pyright-langserver`;
// we probe the langserver flavour since that's what the LSP host
// invokes. scip-python is npm-distributed under
// `@sourcegraph/scip-python`.
type pyDetector struct {
	ex Execer
}

// NewPythonDetector returns the production Python detector.
func NewPythonDetector() Detector {
	return &pyDetector{ex: DefaultExecer}
}

// NewPythonDetectorWithExecer is the test-injection seam.
func NewPythonDetectorWithExecer(ex Execer) Detector {
	return &pyDetector{ex: ex}
}

// LanguageID implements Detector.
func (p *pyDetector) LanguageID() string { return "python" }

// Probe implements Detector.
func (p *pyDetector) Probe(ctx context.Context, workspaceRoot string) (Report, error) {
	report := Report{LanguageID: "python"}

	report.Tools = append(report.Tools, p.probeBinary(ctx, workspaceRoot, pyToolPyright))
	report.Tools = append(report.Tools, p.probeBinary(ctx, workspaceRoot, pyToolScipPython))
	report.Tools = append(report.Tools, ToolReport{
		Name:   "tree-sitter-python",
		Class:  ToolClassParser,
		Status: StatusEmbedded,
		Source: SourceEmbedded,
		ProbeChain: []ProbeStep{
			{Location: "compile-time (cgo)", Found: true, Reason: "github.com/tree-sitter/tree-sitter-python"},
		},
	})

	return report, nil
}

type pyToolSpec struct {
	binaryName string
	class      ToolClass
	serverID   string
	pipxName   string
	npmPackage string
	probeArgs  []string
}

var (
	pyToolPyright = pyToolSpec{
		binaryName: "pyright-langserver",
		class:      ToolClassLSP,
		serverID:   "pyright",
		pipxName:   "pyright",
		npmPackage: "pyright",
		probeArgs:  []string{"--version"},
	}
	pyToolScipPython = pyToolSpec{
		binaryName: "scip-python",
		class:      ToolClassSCIP,
		npmPackage: "@sourcegraph/scip-python",
		probeArgs:  []string{"--version"},
	}
)

// classifyPyStep maps probe-step index to ToolSource.
//
// Steps 0–1 are project_local (.venv / $VIRTUAL_ENV);
// steps 2–4 are ecosystem_user (pipx, uv, ~/.local/bin);
// step 5 is path.
func classifyPyStep(i int) ToolSource {
	switch {
	case i < 2:
		return SourceProjectLocal
	case i < 5:
		return SourceEcosystemUser
	default:
		return SourcePATH
	}
}

func (p *pyDetector) probeBinary(ctx context.Context, workspaceRoot string, spec pyToolSpec) ToolReport {
	tr := ToolReport{
		Name:     spec.binaryName,
		Class:    spec.class,
		ServerID: spec.serverID,
	}

	steps := []ProbeStep{}

	// 1. workspace ./.venv/bin/<tool>
	if workspaceRoot != "" {
		steps = append(steps, probeFilePath(p.ex, filepath.Join(workspaceRoot, ".venv", "bin", spec.binaryName)))
	} else {
		steps = append(steps, ProbeStep{Location: "(workspace .venv/bin)", Found: false, Reason: "no workspace root"})
	}

	// 2. $VIRTUAL_ENV/bin/<tool>
	if ve := p.ex.Getenv("VIRTUAL_ENV"); ve != "" {
		steps = append(steps, probeFilePath(p.ex, filepath.Join(ve, "bin", spec.binaryName)))
	} else {
		steps = append(steps, ProbeStep{Location: "$VIRTUAL_ENV", Found: false, Reason: "VIRTUAL_ENV unset"})
	}

	// 3. pipx-managed venv
	steps = append(steps, p.probePipxBinary(ctx, spec))

	// 4. uv-managed tool venv
	steps = append(steps, p.probeUvBinary(ctx, spec))

	// 5. ~/.local/bin
	if home := p.ex.Getenv("HOME"); home != "" {
		steps = append(steps, probeFilePath(p.ex, filepath.Join(home, ".local", "bin", spec.binaryName)))
	} else {
		steps = append(steps, ProbeStep{Location: "~/.local/bin", Found: false, Reason: "HOME unset"})
	}

	// 6. PATH
	steps = append(steps, probePATH(p.ex, spec.binaryName))

	tr.ProbeChain = steps
	if path, source, ok := firstFound(steps, classifyPyStep); ok {
		tr.Status = StatusAvailable
		tr.Path = path
		tr.Source = source
		if v := versionFromCommand(ctx, p.ex, path, spec.probeArgs...); v != "" {
			tr.Version = v
		}
		return tr
	}

	tr.Status = StatusMissing
	tr.InstallHints = p.installHintsFor(workspaceRoot, spec)
	return tr
}

// probePipxBinary asks pipx for the directory containing the package's
// venv binaries. We parse `pipx list --json` and walk the venvs map.
func (p *pyDetector) probePipxBinary(ctx context.Context, spec pyToolSpec) ProbeStep {
	if spec.pipxName == "" {
		return ProbeStep{Location: "pipx", Found: false, Reason: "tool not pipx-distributed"}
	}
	if _, ok := p.ex.LookPath("pipx"); !ok {
		return ProbeStep{Location: "pipx", Found: false, Reason: "pipx not on PATH"}
	}
	out, err := p.ex.Run(ctx, "pipx", "list", "--json")
	if err != nil {
		return ProbeStep{Location: "pipx list --json", Found: false, Reason: err.Error()}
	}
	type pipxList struct {
		Venvs map[string]struct {
			Metadata struct {
				MainPackage struct {
					AppPaths []struct {
						Path string `json:"__Path__"`
					} `json:"app_paths"`
				} `json:"main_package"`
			} `json:"metadata"`
		} `json:"venvs"`
	}
	var parsed pipxList
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return ProbeStep{Location: "pipx list --json", Found: false, Reason: "parse: " + err.Error()}
	}
	venv, ok := parsed.Venvs[spec.pipxName]
	if !ok {
		return ProbeStep{Location: "pipx list --json", Found: false, Reason: spec.pipxName + " not in pipx venvs"}
	}
	for _, app := range venv.Metadata.MainPackage.AppPaths {
		base := filepath.Base(app.Path)
		if base == spec.binaryName {
			return probeFilePath(p.ex, app.Path)
		}
	}
	return ProbeStep{Location: "pipx list --json", Found: false, Reason: spec.binaryName + " not exposed by pipx venv"}
}

// probeUvBinary checks uv-managed tool venvs. uv tool list output is
// human-formatted; we parse it loosely (one tool per line).
func (p *pyDetector) probeUvBinary(ctx context.Context, spec pyToolSpec) ProbeStep {
	if spec.pipxName == "" {
		return ProbeStep{Location: "uv tool", Found: false, Reason: "tool not uv-distributed"}
	}
	if _, ok := p.ex.LookPath("uv"); !ok {
		return ProbeStep{Location: "uv", Found: false, Reason: "uv not on PATH"}
	}
	// Best-effort: ask uv where it installs tool binaries, then probe.
	binDir, err := p.ex.Run(ctx, "uv", "tool", "dir", "--bin")
	if err != nil || binDir == "" {
		return ProbeStep{Location: "uv tool dir --bin", Found: false, Reason: "uv tool dir unavailable"}
	}
	binDir = strings.TrimSpace(strings.SplitN(binDir, "\n", 2)[0])
	candidate := filepath.Join(binDir, spec.binaryName)
	return probeFilePath(p.ex, candidate)
}

// installHintsFor returns project-aware install commands for missing
// Python tools. Manager preference is pipx → pip → uv → npm fallback;
// scip-python is always npm (npm-distributed upstream). A workspace
// .venv tilts us to pipx regardless of what's on PATH (signals the user
// already runs Python via venvs and most likely has pipx wired up the
// same way).
func (p *pyDetector) installHintsFor(workspaceRoot string, spec pyToolSpec) []InstallHint {
	out := make([]InstallHint, 0, 4)

	// scip-python is npm-distributed even though it operates on Python.
	if spec.binaryName == "scip-python" {
		out = append(out, InstallHint{
			Manager: "npm", Command: "npm i -g " + spec.npmPackage, Preferred: true,
			Reason: "scip-python is distributed on npm",
		})
		return out
	}

	venvDetected := workspaceRoot != "" && dirExists(p.ex, filepath.Join(workspaceRoot, ".venv"))
	_, hasPipx := p.ex.LookPath("pipx")
	_, hasPip := p.ex.LookPath("pip")
	if !hasPip {
		_, hasPip = p.ex.LookPath("pip3")
	}
	_, hasUv := p.ex.LookPath("uv")

	switch {
	case venvDetected:
		out = append(out, InstallHint{
			Manager: "pipx", Command: "pipx install " + spec.pipxName, Preferred: true,
			Reason: ".venv detected in workspace",
		})
	case hasPipx:
		out = append(out, InstallHint{
			Manager: "pipx", Command: "pipx install " + spec.pipxName, Preferred: true,
			Reason: "pipx on PATH",
		})
	case hasPip:
		out = append(out, InstallHint{
			Manager: "pip", Command: "pip install --user " + spec.pipxName, Preferred: true,
			Reason: "pip on PATH",
		})
	case hasUv:
		out = append(out, InstallHint{
			Manager: "uv", Command: "uv tool install " + spec.pipxName, Preferred: true,
			Reason: "uv on PATH",
		})
	default:
		out = append(out, InstallHint{
			Manager: "npm", Command: "npm i -g " + spec.npmPackage, Preferred: true,
			Reason: "no Python package manager detected",
		})
	}
	// Always include the canonical npm fallback as a non-preferred hint.
	out = append(out, InstallHint{
		Manager: "npm", Command: "npm i -g " + spec.npmPackage, Preferred: false, Reason: "fallback",
	})
	return out
}

// ResolveVenv finds the workspace's Python virtualenv root if one
// exists, in the same precedence order detection probes use. Returns
// the absolute venv root (the directory containing bin/), or "" if no
// venv is detected. Used by the LSP pyright driver to pass pythonPath
// in initializationOptions.
//
// Probe order:
//
//	1. workspace ./.venv (most common modern convention)
//	2. workspace ./venv  (older convention)
//	3. $VIRTUAL_ENV
//
// pipx/uv venvs are deliberately excluded — those manage individual
// tools, not the project's runtime interpreter.
func ResolveVenv(ex Execer, workspaceRoot string) string {
	if ex == nil {
		ex = DefaultExecer
	}
	if workspaceRoot != "" {
		for _, name := range []string{".venv", "venv"} {
			candidate := filepath.Join(workspaceRoot, name)
			if dirExists(ex, candidate) && dirExists(ex, filepath.Join(candidate, "bin")) {
				return candidate
			}
		}
	}
	if ve := ex.Getenv("VIRTUAL_ENV"); ve != "" && dirExists(ex, ve) {
		return ve
	}
	return ""
}
