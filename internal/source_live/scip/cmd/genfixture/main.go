//go:build genscipfixture

// Command genfixture (re)generates the canonical SCIP fixture blobs
// committed under tests/testdata/scip/. It is build-tag gated
// (`//go:build genscipfixture`) so the upstream
// github.com/scip-code/scip/bindings/go/scip module enters go.mod only
// when this tool is explicitly built — production code, CI tests, and
// `go test ./...` all stay free of the renamed module.
//
// Run from the repository root:
//
//	go run -tags genscipfixture ./internal/source_live/scip/cmd/genfixture
//
// Reads the upstream encoder version from the go.mod entry and writes
// it into tests/testdata/scip/README.md so the regen command always
// stays in lockstep with the recorded version.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"

	upstream "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func main() {
	repoRoot, err := findRepoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "find repo root:", err)
		os.Exit(1)
	}
	out := filepath.Join(repoRoot, "tests", "testdata", "scip", "sample.scip")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}
	body, err := proto.Marshal(canonicalIndex())
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, body, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	encoderVer := encoderVersion()
	fmt.Printf("wrote %s (%d bytes, encoder=%s)\n", out, len(body), encoderVer)
	fmt.Println()
	fmt.Println("Now update tests/testdata/scip/README.md if the encoder version changed:")
	fmt.Println("  encoder:", encoderVer)
}

// canonicalIndex returns the fixture Index. Keep this shape in lockstep
// with internal/source_live/scip/wire_property_test.go's expectations —
// any change here means regenerating sample.scip.
func canonicalIndex() *upstream.Index {
	return &upstream.Index{
		Metadata: &upstream.Metadata{
			Version:              upstream.ProtocolVersion_UnspecifiedProtocolVersion,
			ProjectRoot:          "file:///workspace/sample",
			ToolInfo:             &upstream.ToolInfo{Name: "scip-go", Version: "0.1.0"},
			TextDocumentEncoding: upstream.TextEncoding_UTF8,
		},
		Documents: []*upstream.Document{
			{
				Language:     "go",
				RelativePath: "pkg/checkout/validator.go",
				Symbols: []*upstream.SymbolInformation{
					{
						Symbol:      "scip-go gomod github.com/foo v1 `pkg/checkout`/Validator#",
						Kind:        upstream.SymbolInformation_Struct,
						DisplayName: "Validator",
					},
					{
						Symbol:      "scip-go gomod github.com/foo v1 `pkg/checkout`/Validator#Validate().",
						Kind:        upstream.SymbolInformation_Method,
						DisplayName: "Validate",
					},
					{
						Symbol:      "scip-go gomod github.com/foo v1 `pkg/checkout`/Helper().",
						Kind:        upstream.SymbolInformation_Function,
						DisplayName: "Helper",
					},
				},
				Occurrences: []*upstream.Occurrence{
					{
						Range:       []int32{3, 0, 5, 4},
						Symbol:      "scip-go gomod github.com/foo v1 `pkg/checkout`/Validator#Validate().",
						SymbolRoles: int32(upstream.SymbolRole_Definition),
					},
					{
						Range:       []int32{8, 0, 10, 4},
						Symbol:      "scip-go gomod github.com/foo v1 `pkg/checkout`/Helper().",
						SymbolRoles: int32(upstream.SymbolRole_Definition),
					},
				},
			},
		},
	}
}

// findRepoRoot returns the main module's repository root by climbing
// out of this file's location. The genfixture tool lives in its own
// sub-module (so the upstream encoder dep doesn't leak into the main
// go.mod), which means walking up from cwd would find this submodule's
// go.mod first — we want the repo root four levels up from this file
// (cmd/genfixture → scip/cmd → scip → source_live → internal → repo).
func findRepoRoot() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller failed")
	}
	// thisFile = .../internal/source_live/scip/cmd/genfixture/main.go
	// repo root is six levels up: genfixture/..(=cmd) /.. (=scip) /..
	// (=source_live) /.. (=internal) /.. (=repo).
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", fmt.Errorf("expected main go.mod at %s: %w", root, err)
	}
	return root, nil
}

// encoderVersion returns the pinned upstream module version this tool
// was built against, so the fixture README stays accurate.
func encoderVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range info.Deps {
		if dep.Path == "github.com/scip-code/scip/bindings/go/scip" {
			return dep.Version
		}
	}
	return "unknown"
}
