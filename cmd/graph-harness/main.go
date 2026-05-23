// Command graph-harness is the single CLI surface for the Graph Harness daemon
// (SPEC §9.2). Phase 0 wires the subcommand surface end-to-end. Stages still
// in progress print the populating P0.T task ID and exit non-zero so progress
// is visible from the help output.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/cli"

	// P2.T03 — activate every compiled-in framework extractor by
	// blank-importing the aggregator. Extractor packages register
	// themselves with the code.framework registry via init().
	_ "github.com/shivamstaq/graph-harness/extractors/all"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		// Cobra prints the error itself when SilenceErrors is false; we
		// silence to keep output deterministic and only echo here.
		fmt.Fprintln(os.Stderr, "graph-harness:", err)
		os.Exit(1)
	}
}

// Compile-time assertion the cobra reference stays valid.
var _ = (*cobra.Command)(nil)
