package detect

import (
	"context"
	"path/filepath"
	"strings"
)

// goDetector probes the Go ecosystem: gopls (LSP) and scip-go (SCIP).
// Tree-sitter-go is cgo-bundled into the binary so its ToolReport is
// always Status = StatusEmbedded.
//
// Probe order per SPEC §6.18:
//   1. workspace-local vendor/.../bin/<tool> (rare; some monorepos)
//   2. $(go env GOBIN)/<tool>
//   3. $(go env GOPATH)/bin/<tool>
//   4. $PATH
type goDetector struct {
	ex Execer
}

// NewGoDetector returns the production Go detector.
func NewGoDetector() Detector {
	return &goDetector{ex: DefaultExecer}
}

// NewGoDetectorWithExecer is the test-injection seam.
func NewGoDetectorWithExecer(ex Execer) Detector {
	return &goDetector{ex: ex}
}

// LanguageID implements Detector.
func (g *goDetector) LanguageID() string { return "go" }

// Probe implements Detector.
func (g *goDetector) Probe(ctx context.Context, workspaceRoot string) (Report, error) {
	report := Report{LanguageID: "go"}

	// gopls — LSP
	report.Tools = append(report.Tools, g.probeBinary(ctx, workspaceRoot, goToolGopls))
	// scip-go — SCIP indexer
	report.Tools = append(report.Tools, g.probeBinary(ctx, workspaceRoot, goToolScip))
	// tree-sitter-go — embedded
	report.Tools = append(report.Tools, ToolReport{
		Name:   "tree-sitter-go",
		Class:  ToolClassParser,
		Status: StatusEmbedded,
		Source: SourceEmbedded,
		ProbeChain: []ProbeStep{
			{Location: "compile-time (cgo)", Found: true, Reason: "github.com/tree-sitter/tree-sitter-go"},
		},
	})

	return report, nil
}

type goToolSpec struct {
	binaryName string
	class      ToolClass
	serverID   string
	installCmd string // canonical install hint
	probeArgs  []string
}

var (
	goToolGopls = goToolSpec{
		binaryName: "gopls",
		class:      ToolClassLSP,
		serverID:   "gopls",
		installCmd: "go install golang.org/x/tools/gopls@latest",
		probeArgs:  []string{"version"},
	}
	goToolScip = goToolSpec{
		binaryName: "scip-go",
		class:      ToolClassSCIP,
		// SPEC §6.17 noted the upstream `sourcegraph/scip-go` →
		// `scip-code/scip-go` repo rename mid-2025; the rename is
		// now the officially-supported install path. Old
		// sourcegraph/scip-go redirects, but new docs and the
		// canonical `go install` recipe both use scip-code.
		installCmd: "go install github.com/scip-code/scip-go/cmd/scip-go@latest",
		probeArgs:  []string{"--version"},
	}
)

func (g *goDetector) probeBinary(ctx context.Context, workspaceRoot string, spec goToolSpec) ToolReport {
	tr := ToolReport{
		Name:     spec.binaryName,
		Class:    spec.class,
		ServerID: spec.serverID,
	}

	classify := func(i int) ToolSource {
		switch i {
		case 0:
			return SourceProjectLocal
		case 1, 2:
			return SourceEcosystemUser
		default:
			return SourcePATH
		}
	}

	// 1. workspace-local vendor/bin (rare but legit in monorepos using go modules with replace pinning).
	steps := []ProbeStep{}
	if workspaceRoot != "" {
		vendoredPath := filepath.Join(workspaceRoot, "vendor", "bin", spec.binaryName)
		steps = append(steps, probeFilePath(g.ex, vendoredPath))
	} else {
		steps = append(steps, ProbeStep{Location: "(workspace vendor/bin)", Found: false, Reason: "no workspace root"})
	}

	// 2 + 3. go env GOBIN / GOPATH/bin.
	gobin, gopath := g.goEnvDirs(ctx)
	if gobin != "" {
		steps = append(steps, probeFilePath(g.ex, filepath.Join(gobin, spec.binaryName)))
	} else {
		steps = append(steps, ProbeStep{Location: "$(go env GOBIN)", Found: false, Reason: "GOBIN unset"})
	}
	if gopath != "" {
		steps = append(steps, probeFilePath(g.ex, filepath.Join(gopath, "bin", spec.binaryName)))
	} else {
		steps = append(steps, ProbeStep{Location: "$(go env GOPATH)/bin", Found: false, Reason: "GOPATH unresolvable"})
	}

	// 4. PATH.
	steps = append(steps, probePATH(g.ex, spec.binaryName))

	tr.ProbeChain = steps
	if path, source, ok := firstFound(steps, classify); ok {
		tr.Status = StatusAvailable
		tr.Path = path
		tr.Source = source
		// Best-effort version probe; ignore failures.
		if v := versionFromCommand(ctx, g.ex, path, spec.probeArgs...); v != "" {
			tr.Version = simplifyGoVersion(spec.binaryName, v)
		}
		return tr
	}

	tr.Status = StatusMissing
	tr.InstallHints = []InstallHint{{
		Manager:   "go install",
		Command:   spec.installCmd,
		Preferred: true,
		Reason:    "go toolchain present",
	}}
	return tr
}

// goEnvDirs queries `go env GOBIN GOPATH`. Returns ("", "") when go is
// not on $PATH (or `go env` errors); the probe chain records that as a
// missed step rather than a hard failure.
func (g *goDetector) goEnvDirs(ctx context.Context) (gobin, gopath string) {
	if _, ok := g.ex.LookPath("go"); !ok {
		return "", ""
	}
	out, err := g.ex.Run(ctx, "go", "env", "GOBIN", "GOPATH")
	if err != nil {
		return "", ""
	}
	lines := strings.Split(out, "\n")
	if len(lines) >= 1 {
		gobin = strings.TrimSpace(lines[0])
	}
	if len(lines) >= 2 {
		gopath = strings.TrimSpace(lines[1])
	}
	return gobin, gopath
}

// simplifyGoVersion strips noise from `gopls version` (multi-line) and
// `scip-go --version` so the doctor table stays compact.
func simplifyGoVersion(name, raw string) string {
	first := strings.SplitN(raw, "\n", 2)[0]
	first = strings.TrimSpace(first)
	switch name {
	case "gopls":
		// "golang.org/x/tools/gopls v0.16.1"
		idx := strings.LastIndex(first, "v")
		if idx >= 0 {
			return first[idx:]
		}
	case "scip-go":
		// "scip-go version 0.1.20"
		fields := strings.Fields(first)
		if len(fields) > 0 {
			return fields[len(fields)-1]
		}
	}
	return first
}
