package lsp

// NewPyrightDriver returns a Driver that talks to
// `pyright-langserver --stdio`. Pyright has a multi-second cold start
// (the server type-checks the workspace on initialize), so the Host
// keeps it lazy: we only spawn pyright when the first .py file is
// queried.
//
// We pass {"openFilesOnly": true, "useLibraryCodeForTypes": true} via
// initializationOptions so pyright doesn't index the whole workspace
// up front; the open-files-only mode keeps cold-start sub-second on
// most repos and matches the SPEC §6.11 "live" freshness profile.
func NewPyrightDriver() Driver {
	return newGenericDriver(driverConf{
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
	})
}
