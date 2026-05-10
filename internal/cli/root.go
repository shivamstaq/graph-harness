// Package cli wires every Phase-0 CLI subcommand to its implementation.
// The root command surface stays stable across Phase 0; individual commands
// graduate from "stub returning P0.T<NN>" to real behavior as their tasks
// land.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// NewRootCmd returns the top-level cobra.Command tree. Every leaf subcommand
// either runs real code (P0.T<NN> complete) or prints the populating task ID
// and exits non-zero (still in progress).
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "graph-harness",
		Short:         "Semantic layer for codebases — kernel + N layers over a single event log",
		Long:          "graph-harness is the unified CLI for the Graph Harness daemon. See SPEC.md and DESIGN.md.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newInitCmd(),
		newStatusCmd(),
		newLayersCmd(),
		newDaemonCmd(),
		newQueryCmd(),
		newCodeCmd(),
		newSelectorsCmd(),
		newFlowsCmd(),
		newValidateDiffCmd(),
		newReplCmd(),
		newTUICmd(),
		newStudioCmd(),
		newReviewCmd(),
		newBenchCmd(),
		newMCPCmd(),
		newDoctorCmd(),
		newVersionCmd(),
	)
	return root
}

// stub prints the populating task ID and returns a non-zero exit. As tasks
// land, each call site graduates to a real RunE.
func stub(taskID string) func(cmd *cobra.Command, _ []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"%q is not implemented yet (Phase 0 task %s)\n",
			cmd.CommandPath(), taskID)
		return fmt.Errorf("not implemented in P0 task %s", taskID)
	}
}
