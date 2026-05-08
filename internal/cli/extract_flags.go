package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/extract"
)

// addExtractorToggleFlags registers the --no-lsp / --no-scip /
// --no-treesitter flags on cmd. The flags ride alongside any
// command that triggers indexWorkspaceCode + the extract
// orchestrator, so e2e specs can exercise SPEC §6.11
// build-order-tolerance modes (single-source ingestion) and the
// three-source disagreement gates without recompiling the binary.
//
// Defaults reflect production wiring:
//
//   - tree-sitter: ON (cheap; primary source of truth).
//   - SCIP:        ON (cheap file-read; only loads when
//     .scip-index/ exists).
//   - LSP:         OFF unless GRAPH_HARNESS_ENABLE_LSP=1 is set
//     (cold-start latency makes per-CLI-invocation
//     spawning impractical for `selectors test` /
//     `validate-diff`; the daemon's long-lived
//     background indexer owns the persistent host).
//
// Pass-through flags override the defaults in either direction:
// --no-lsp / --no-scip / --no-treesitter unconditionally disable
// the named source for this invocation.
func addExtractorToggleFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("no-lsp", false, "disable LSP DocumentSymbol ingestion for this invocation")
	cmd.Flags().Bool("no-scip", false, "disable SCIP index ingestion for this invocation")
	cmd.Flags().Bool("no-treesitter", false, "disable tree-sitter parsing for this invocation (build-order-tolerance test mode)")
}

// extractOptionsFromFlags reads the toggle flags and the
// GRAPH_HARNESS_DISABLE_LSP env var into an extract.Options. CLI
// callers that have wired addExtractorToggleFlags use this to build
// the orchestrator's option set. LSP is enabled by default — the
// orchestrator's bounded fast-fail (lspInitTimeout / lspPerCallTimeout
// in internal/extract/orchestrator.go) protects against cold-start
// stalls. Opt out per-invocation with --no-lsp or globally via
// GRAPH_HARNESS_DISABLE_LSP=1.
func extractOptionsFromFlags(cmd *cobra.Command) extract.Options {
	noLSP, _ := cmd.Flags().GetBool("no-lsp")
	noSCIP, _ := cmd.Flags().GetBool("no-scip")
	noTS, _ := cmd.Flags().GetBool("no-treesitter")
	envDisableLSP := os.Getenv("GRAPH_HARNESS_DISABLE_LSP") == "1"
	return extract.Options{
		DisableLSP:        noLSP || envDisableLSP,
		DisableSCIP:       noSCIP,
		DisableTreesitter: noTS,
	}
}
