package lsp

import (
	"context"
	"path/filepath"

	"github.com/shivamstaq/graph-harness/internal/source_live/detect"
)

// NewPyrightDriver returns a Driver that talks to
// `pyright-langserver --stdio`. Pyright has a multi-second cold start
// (the server type-checks the workspace on initialize), so the Host
// keeps it lazy: we only spawn pyright when the first .py file is
// queried.
//
// initializationOptions:
//   - openFilesOnly + useLibraryCodeForTypes + diagnosticMode keep
//     pyright from indexing the whole workspace up front; the
//     open-files-only mode keeps cold-start sub-second on most repos
//     and matches the SPEC §6.11 "live" freshness profile.
//   - python.pythonPath is resolved via detect.ResolveVenv at spawn
//     time so pyright type-checks against the project's own
//     virtualenv (.venv/, venv/, $VIRTUAL_ENV) rather than the system
//     interpreter. Closes phase-1-gaps.md §4.3 and prevents a flood of
//     spurious SymbolDisambiguation events on Python repos with
//     project-local dependencies.
//
// The Driver's root is the workspace root (Host.NewHost rooting); we
// deliberately don't read it from the env at construction time — the
// venv may live below the root that the user opens.
func NewPyrightDriver() Driver {
	return &pyrightDriver{
		genericDriver: newGenericDriver(driverConf{
			languageID: "python",
			executable: "pyright-langserver",
			args:       []string{"--stdio"},
			producedBy: "extractor:lsp:pyright",
			initOpts: map[string]any{
				"python": map[string]any{
					"analysis": map[string]any{
						"openFilesOnly":          true,
						"useLibraryCodeForTypes": true,
						"diagnosticMode":         "openFilesOnly",
					},
				},
			},
		}),
	}
}

// pyrightDriver wraps the generic driver to inject pythonPath into
// initializationOptions at Initialize time. We override Initialize so
// the workspace root passed in by Host.DriverFor can be resolved
// against detect.ResolveVenv.
type pyrightDriver struct {
	*genericDriver
}

// Initialize resolves the workspace's Python venv (if any) and merges
// pythonPath into initializationOptions before delegating to the
// embedded generic driver.
func (p *pyrightDriver) Initialize(ctx context.Context, root string) error {
	if venv := detect.ResolveVenv(detect.DefaultExecer, root); venv != "" {
		p.genericDriver.conf.initOpts = mergePyrightInitOpts(
			p.genericDriver.conf.initOpts,
			filepath.Join(venv, "bin", "python"),
		)
	}
	return p.genericDriver.Initialize(ctx, root)
}

// mergePyrightInitOpts copies opts (expected to be the
// `python.analysis.*` map produced by NewPyrightDriver) and adds
// `python.pythonPath`. The returned map is independent so concurrent
// pyright spawns don't share state. opts may be nil.
func mergePyrightInitOpts(opts any, pythonPath string) map[string]any {
	out := map[string]any{}
	if existing, ok := opts.(map[string]any); ok {
		for k, v := range existing {
			out[k] = v
		}
	}
	pyBlock, ok := out["python"].(map[string]any)
	if !ok {
		pyBlock = map[string]any{}
	} else {
		// Copy so we don't mutate the original block.
		copied := make(map[string]any, len(pyBlock))
		for k, v := range pyBlock {
			copied[k] = v
		}
		pyBlock = copied
	}
	pyBlock["pythonPath"] = pythonPath
	out["python"] = pyBlock
	return out
}
