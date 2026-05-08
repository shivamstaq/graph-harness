package helpers

import (
	_ "embed"
	"os"
	"path/filepath"

	"github.com/shivamstaq/gotit/runner"
)

// scipSampleBlob carries the canonical SCIP wire-format fixture
// committed at tests/testdata/scip/sample.scip. Embedded at compile
// time so e2e helpers can stage it into a workspace's `.scip-index/`
// without depending on a runtime repo-root env var (gotit doesn't
// expose one). Regenerate via `go run -tags genscipfixture
// ./internal/source_live/scip/cmd/genfixture` per the SPEC §6.17 path.
//
//go:embed scip_sample.scip
var scipSampleBlob []byte

// GoModuleWithCheckoutValidatorAndSCIPIndex builds the canonical Go
// checkout fixture (same as GoModuleWithCheckoutValidator) and then
// stages the committed SCIP wire-format blob under `.scip-index/`.
// Used by phase1/source-live/scip-import-replays-events to verify the
// full SCIP → unifier → code.core path lands entities + index_scip
// provenance without requiring scip-go on the test host.
//
// The path inside the fixture (pkg/checkout/validator.go) deliberately
// differs from the helper's source tree (internal/checkout/validator.go)
// so the SCIP-imported entities don't collide with tree-sitter
// entities — making the SCIP source attribution unambiguous in the
// assertions.
func GoModuleWithCheckoutValidatorAndSCIPIndex(probe, workDir string, params map[string]any) error {
	if err := GoModuleWithCheckoutValidator(probe, workDir, params); err != nil {
		return err
	}
	dir := filepath.Join(workDir, ".scip-index")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return runner.WriteFile(filepath.Join(dir, "checkout.scip"), string(scipSampleBlob))
}
