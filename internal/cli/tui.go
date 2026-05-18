package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
	"github.com/shivamstaq/graph-harness/internal/tui"
)

// newTUICmdReal implements `graph-harness tui`. Per F2 /
// plan/answers/04 §5, when a daemon is running, TUI dials it over
// JSON-RPC; otherwise it opens local Resources in batch mode. The
// bubbletea cockpit consumes the daemon via the jsonrpc.Consumer
// interface — both backends are transparent to the model.
//
// `--interactive=false` (the default for CI specs) prints a single
// status snapshot via the same Consumer and exits, so gotit specs
// can run without a PTY and the daemon-pid-stability check sees a
// routed call.
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

			handle, err := ResolveRoute(ctx, ws, RouteOptions{})
			if err != nil {
				return err
			}
			defer func() { _ = handle.Close() }()

			var svc jsonrpc.Consumer
			if handle.Mode == ModeDaemon {
				svc = jsonrpc.NewClientService(handle.Client)
			} else {
				// Batch fallback — open local Resources. The TUI is read-
				// only at this layer; OverlaySave goes through the
				// in-process *Service so writes still respect strict
				// trust-policy enforcement.
				res, err := daemon.Open(ctx, ws)
				if err != nil {
					return err
				}
				defer func() { _ = res.Close() }()
				svc = jsonrpc.NewService(ws, res.Log, res.Code, res.Queue, res.Registry, res.Overlay)
			}

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
