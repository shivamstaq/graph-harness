package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/version"
)

// Stub commands. Each leaf prints the populating P0.T task and exits 1.
// As tasks land, the commands graduate out of this file.

func newDaemonCmd() *cobra.Command { return newDaemonCmdReal() }

func newQueryCmd() *cobra.Command {
	c := newQueryCmdReal()
	c.Flags().String("json", "", "read query AST from a JSON file")
	return c
}

func newReplCmd() *cobra.Command { return newReplCmdReal() }

func newSelectorsCmd() *cobra.Command {
	c := &cobra.Command{Use: "selectors", Short: "Selector authoring + resolution tools"}
	// newSelectorsTestCmd already wires --batch internally.
	c.AddCommand(newSelectorsTestCmd())
	return c
}

// addBatchFlag wires the --batch flag onto a read-side cobra command.
// Read commands that opt into batch mode (P0.5.T16 / SPEC §9.11) open
// SQLite read-only, pin to the kernel head, and skip the daemon.
// Setting GRAPH_HARNESS_BATCH=1 has the same effect at the environment
// level. The flag is wired here (rather than per command) so the
// surface stays uniform and the route helper can read it via
// cmd.Flags().GetBool("batch").
func addBatchFlag(c *cobra.Command) {
	c.Flags().Bool("batch", false, "open layer SQLite read-only and skip the daemon (P0.5.T16 / SPEC §9.11; same effect as GRAPH_HARNESS_BATCH=1)")
}

func newFlowsCmd() *cobra.Command {
	c := &cobra.Command{Use: "flows", Short: "Manage flow declarations in semantic.overlay"}
	fl := newFlowsListCmd()
	addBatchFlag(fl)
	c.AddCommand(
		fl,
		newFlowsCreateCmd(),
		newFlowsEditCmd(),
	)
	return c
}

func newValidateDiffCmd() *cobra.Command {
	c := newValidateDiffCmdReal()
	c.Flags().String("diff", "", "path to a unified-diff file (default: stdin)")
	c.Flags().String("against-plan", "", "validate against an explicit plan.gh")
	c.Flags().String("finding", "", "re-run validation for a specific finding ID")
	c.Flags().Bool("json", false, "emit findings as JSON")
	addBatchFlag(c)
	addExtractorToggleFlags(c)
	return c
}

func newTUICmd() *cobra.Command {
	c := newTUICmdReal()
	c.Flags().Bool("interactive", false, "block in TUI loop (when false, prints status and exits)")
	return c
}

func newStudioCmd() *cobra.Command {
	c := newStudioCmdReal()
	c.Flags().Int("port", 0, "loopback port (0 = ephemeral)")
	c.Flags().Bool("oneshot", false, "bind, print URL, then exit (CI-friendly)")
	c.Flags().Bool("check", false, "resolve the consumer and exit 0 (smoke surface for daemon-routed CI)")
	return c
}

func newReviewCmd() *cobra.Command { return newReviewCmdReal() }

func newBenchCmd() *cobra.Command { return newBenchCmdReal() }

func newMCPCmd() *cobra.Command { return newMCPCmdReal() }

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), version.String())
			return nil
		},
	}
}
