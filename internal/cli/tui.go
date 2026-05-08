package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
	"github.com/shivamstaq/graph-harness/internal/tui"
)

// newTUICmdReal implements `graph-harness tui`. Launches the bubbletea
// cockpit bound to an in-process Service (no daemon round-trip required —
// the TUI is a read-only consumer of kernel state and benefits from the
// shared SQLite handles already opened by the CLI).
//
// When --interactive=false (the default for CI specs) the command prints a
// single status snapshot and exits, so gotit specs can run without a PTY.
func newTUICmdReal() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Launch the terminal cockpit",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			res, err := daemon.Open(ctx, ws)
			if err != nil {
				return err
			}
			defer func() { _ = res.Close() }()

			svc := jsonrpc.NewService(ws, res.Log, res.Code, res.Queue, res.Registry, res.Overlay)
			interactive, _ := cmd.Flags().GetBool("interactive")
			if !interactive {
				st, err := svc.Status(ctx)
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				_, _ = fmt.Fprintln(out, "─── graph-harness TUI (snapshot) ──────")
				_, _ = fmt.Fprintf(out, "Workspace:    %s\n", st.WorkspaceRoot)
				_, _ = fmt.Fprintf(out, "Workspace ID: %s\n", st.WorkspaceID)
				_, _ = fmt.Fprintf(out, "Event log:    %s\n", st.EventLogPath)
				_, _ = fmt.Fprintf(out, "Last seq:     %d\n", st.LastSeq)
				_, _ = fmt.Fprintf(out, "Overlay decls: %d\n", st.OverlayCount)
				_, _ = fmt.Fprintln(out, "Findings:    (run `graph-harness validate-diff` and re-launch with --interactive to view)")
				_, _ = fmt.Fprintln(out, "Conflicts:   (no SymbolDisambiguation events recorded yet)")
				_, _ = fmt.Fprintln(out, "tabs: workspace · findings · conflicts (use --interactive for the live view)")
				return nil
			}
			model := tui.NewModel(svc)
			return model.Run(context.Background())
		},
	}
}
