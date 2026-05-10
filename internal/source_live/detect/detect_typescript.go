package detect

import (
	"context"
	"path/filepath"
	"strings"
)

// tsDetector probes the TypeScript ecosystem: typescript-language-server
// (LSP) and scip-typescript (SCIP). Special case: if the workspace has
// deno.json or deno.jsonc the canonical server is `deno lsp` (a separate
// language id `deno`); we still surface tsserver/scip-typescript here
// for any non-Deno TS code in the same workspace.
//
// Probe order per SPEC §6.18:
//   1. workspace ./node_modules/.bin/<tool>           (project_local)
//   2. workspace `bun pm bin`/<tool>                  (project_local — bun-resolved)
//   3. workspace `pnpm bin`/<tool>                    (project_local — pnpm-resolved)
//   4. workspace `yarn bin`/<tool>                    (project_local — yarn-resolved)
//   5. `bun pm bin -g`/<tool>                         (ecosystem_user)
//   6. `pnpm bin -g`/<tool>                           (ecosystem_user)
//   7. `yarn global bin`/<tool>                       (ecosystem_user)
//   8. `npm root -g`/../bin/<tool>                    (ecosystem_user)
//   9. $PATH
type tsDetector struct {
	ex Execer
}

// NewTypeScriptDetector returns the production TypeScript detector.
func NewTypeScriptDetector() Detector {
	return &tsDetector{ex: DefaultExecer}
}

// NewTypeScriptDetectorWithExecer is the test-injection seam.
func NewTypeScriptDetectorWithExecer(ex Execer) Detector {
	return &tsDetector{ex: ex}
}

// LanguageID implements Detector.
func (t *tsDetector) LanguageID() string { return "typescript" }

// Probe implements Detector.
func (t *tsDetector) Probe(ctx context.Context, workspaceRoot string) (Report, error) {
	report := Report{LanguageID: "typescript"}

	// LSP — typescript-language-server.
	report.Tools = append(report.Tools, t.probeBinary(ctx, workspaceRoot, tsToolTSServer))
	// SCIP — scip-typescript.
	report.Tools = append(report.Tools, t.probeBinary(ctx, workspaceRoot, tsToolScip))
	// tree-sitter-typescript — embedded.
	report.Tools = append(report.Tools, ToolReport{
		Name:   "tree-sitter-typescript",
		Class:  ToolClassParser,
		Status: StatusEmbedded,
		Source: SourceEmbedded,
		ProbeChain: []ProbeStep{
			{Location: "compile-time (cgo)", Found: true, Reason: "github.com/tree-sitter/tree-sitter-typescript"},
		},
	})

	return report, nil
}

type tsToolSpec struct {
	binaryName string
	class      ToolClass
	serverID   string
	npmPackage string // for install hints
	probeArgs  []string
}

var (
	tsToolTSServer = tsToolSpec{
		binaryName: "typescript-language-server",
		class:      ToolClassLSP,
		serverID:   "typescript-language-server",
		npmPackage: "typescript-language-server typescript",
		probeArgs:  []string{"--version"},
	}
	tsToolScip = tsToolSpec{
		binaryName: "scip-typescript",
		class:      ToolClassSCIP,
		npmPackage: "@sourcegraph/scip-typescript",
		probeArgs:  []string{"--version"},
	}
)

// classifyTSStep maps probe-step index to ToolSource.
//
// Layout is fixed (probePackageManagerBin always emits 2 steps):
//
//	[0]      workspace ./node_modules/.bin            project_local
//	[1..2]   workspace bun pm bin (discovery, binary) project_local
//	[3..4]   workspace pnpm bin                       project_local
//	[5..6]   workspace yarn bin                       project_local
//	[7..8]   global    bun pm bin -g                  ecosystem_user
//	[9..10]  global    pnpm bin -g                    ecosystem_user
//	[11..12] global    yarn global bin                ecosystem_user
//	[13]     npm root -g/../bin                       ecosystem_user
//	[14]     $PATH                                    path
func classifyTSStep(i int) ToolSource {
	switch {
	case i < 7:
		return SourceProjectLocal
	case i < 14:
		return SourceEcosystemUser
	default:
		return SourcePATH
	}
}

func (t *tsDetector) probeBinary(ctx context.Context, workspaceRoot string, spec tsToolSpec) ToolReport {
	tr := ToolReport{
		Name:     spec.binaryName,
		Class:    spec.class,
		ServerID: spec.serverID,
	}

	steps := []ProbeStep{}

	// 1. workspace ./node_modules/.bin
	if workspaceRoot != "" {
		steps = append(steps, probeFilePath(t.ex, filepath.Join(workspaceRoot, "node_modules", ".bin", spec.binaryName)))
	} else {
		steps = append(steps, ProbeStep{Location: "(workspace ./node_modules/.bin)", Found: false, Reason: "no workspace root"})
	}

	// 2-4. workspace bun/pnpm/yarn bin (project-resolved global-vs-project bin discovery).
	steps = append(steps, t.probePackageManagerBin(ctx, workspaceRoot, "bun", []string{"pm", "bin"}, spec.binaryName)...)
	steps = append(steps, t.probePackageManagerBin(ctx, workspaceRoot, "pnpm", []string{"bin"}, spec.binaryName)...)
	steps = append(steps, t.probePackageManagerBin(ctx, workspaceRoot, "yarn", []string{"bin"}, spec.binaryName)...)

	// 5-8. ecosystem-user globals: bun -g, pnpm -g, yarn global, npm root -g + /../bin.
	steps = append(steps, t.probePackageManagerBin(ctx, "", "bun", []string{"pm", "bin", "-g"}, spec.binaryName)...)
	steps = append(steps, t.probePackageManagerBin(ctx, "", "pnpm", []string{"bin", "-g"}, spec.binaryName)...)
	steps = append(steps, t.probePackageManagerBin(ctx, "", "yarn", []string{"global", "bin"}, spec.binaryName)...)
	steps = append(steps, t.probeNpmGlobalBin(ctx, spec.binaryName))

	// 9. $PATH
	steps = append(steps, probePATH(t.ex, spec.binaryName))

	tr.ProbeChain = steps
	if path, source, ok := firstFound(steps, classifyTSStep); ok {
		tr.Status = StatusAvailable
		tr.Path = path
		tr.Source = source
		if v := versionFromCommand(ctx, t.ex, path, spec.probeArgs...); v != "" {
			tr.Version = v
		}
		return tr
	}

	tr.Status = StatusMissing
	tr.InstallHints = t.installHintsFor(workspaceRoot, spec.npmPackage)
	return tr
}

// probePackageManagerBin runs `<manager> <args...>` to obtain a bin
// directory and probes for the binary inside. Always returns exactly
// two ProbeSteps so the caller can rely on fixed indexing for
// source-class classification:
//
//   - [0] discovery step (Found=false; informational — what dir the
//     manager reports, or why we couldn't ask it)
//   - [1] binary-presence step (Found=true iff <dir>/<binary> exists)
//
// cwd is the directory the manager would be invoked in (currently used
// only as part of the discovery label since we don't yet plumb cwd
// through Execer.Run; project-local probes assume graph-harness was
// invoked from inside the workspace).
func (t *tsDetector) probePackageManagerBin(ctx context.Context, cwd, manager string, args []string, binary string) []ProbeStep {
	label := managerLabel(manager, args, cwd)
	if _, ok := t.ex.LookPath(manager); !ok {
		return []ProbeStep{
			{Location: label, Found: false, Reason: manager + " not on PATH"},
			{Location: label + " → <skipped>", Found: false, Reason: manager + " not on PATH"},
		}
	}
	discovery, dir := probeRunForBin(ctx, t.ex, label, manager, args...)
	if dir == "" {
		return []ProbeStep{
			discovery,
			{Location: label + " → <unresolved>", Found: false, Reason: "no bin dir"},
		}
	}
	binStep := probeFilePath(t.ex, filepath.Join(dir, binary))
	return []ProbeStep{discovery, binStep}
}

// probeNpmGlobalBin computes `npm root -g` → dirname → /bin/<binary>.
// `npm bin -g` was removed in npm 9; this is the forward-compatible
// path.
func (t *tsDetector) probeNpmGlobalBin(ctx context.Context, binary string) ProbeStep {
	if _, ok := t.ex.LookPath("npm"); !ok {
		return ProbeStep{Location: "npm root -g", Found: false, Reason: "npm not on PATH"}
	}
	out, err := t.ex.Run(ctx, "npm", "root", "-g")
	if err != nil {
		return ProbeStep{Location: "npm root -g", Found: false, Reason: err.Error()}
	}
	if out == "" {
		return ProbeStep{Location: "npm root -g", Found: false, Reason: "empty output"}
	}
	// `npm root -g` returns the global node_modules dir; binaries live
	// in its sibling /bin (e.g. /usr/lib/node_modules → /usr/lib/bin
	// usually, but practically /usr/bin via the symlink).
	root := out
	parent := filepath.Dir(root)
	candidate := filepath.Join(parent, "bin", binary)
	return probeFilePath(t.ex, candidate)
}

// installHintsFor returns the project-aware install commands. We pick
// the manager that matches the workspace's lockfile; absent any
// lockfile, npm is the safest default.
func (t *tsDetector) installHintsFor(workspaceRoot, npmPackage string) []InstallHint {
	manager, reason := preferredJSManager(t.ex, workspaceRoot)
	hints := make([]InstallHint, 0, 4)
	switch manager {
	case "bun":
		hints = append(hints, InstallHint{Manager: "bun", Command: "bun add -g " + npmPackage, Preferred: true, Reason: reason})
	case "pnpm":
		hints = append(hints, InstallHint{Manager: "pnpm", Command: "pnpm add -g " + npmPackage, Preferred: true, Reason: reason})
	case "yarn":
		hints = append(hints, InstallHint{Manager: "yarn", Command: "yarn global add " + npmPackage, Preferred: true, Reason: reason})
	default:
		hints = append(hints, InstallHint{Manager: "npm", Command: "npm i -g " + npmPackage, Preferred: true, Reason: reason})
	}
	// Always include the canonical npm fallback for users with no JS
	// project context.
	if manager != "npm" {
		hints = append(hints, InstallHint{Manager: "npm", Command: "npm i -g " + npmPackage, Preferred: false, Reason: "fallback"})
	}
	return hints
}

// preferredJSManager inspects the workspace for lockfile evidence of
// the chosen package manager. Returns one of bun/pnpm/yarn/npm and a
// reason string suitable for InstallHint.Reason.
func preferredJSManager(ex Execer, workspaceRoot string) (string, string) {
	if workspaceRoot == "" {
		return "npm", "no workspace root; npm fallback"
	}
	if fileExists(ex, filepath.Join(workspaceRoot, "bun.lockb")) || fileExists(ex, filepath.Join(workspaceRoot, "bun.lock")) {
		return "bun", "bun.lockb detected"
	}
	if fileExists(ex, filepath.Join(workspaceRoot, "pnpm-lock.yaml")) {
		return "pnpm", "pnpm-lock.yaml detected"
	}
	if fileExists(ex, filepath.Join(workspaceRoot, "yarn.lock")) {
		return "yarn", "yarn.lock detected"
	}
	if fileExists(ex, filepath.Join(workspaceRoot, "package-lock.json")) {
		return "npm", "package-lock.json detected"
	}
	if fileExists(ex, filepath.Join(workspaceRoot, "package.json")) {
		return "npm", "package.json (no lockfile)"
	}
	return "npm", "no JS lockfile; npm fallback"
}

// managerLabel formats a probe-step label like
// `bun pm bin (workspace)` or `pnpm bin -g (global)`.
func managerLabel(manager string, args []string, cwd string) string {
	parts := append([]string{manager}, args...)
	scope := "global"
	if cwd != "" {
		scope = "workspace"
	}
	return joinFields(parts) + " (" + scope + ")"
}

func joinFields(parts []string) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(p)
	}
	return b.String()
}
