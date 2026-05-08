package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
	"github.com/shivamstaq/graph-harness/internal/studio"
)

// newStudioCmdReal implements `graph-harness studio`. Loopback-only HTTP
// server with per-session token + strict Origin check (SPEC §9.6 — these
// protections are non-negotiable). Backed by the in-process Service so it
// shares the workspace's code.core / overlay handles.
func newStudioCmdReal() *cobra.Command {
	return &cobra.Command{
		Use:   "studio",
		Short: "Launch the local-first web UI (loopback only)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			port, _ := cmd.Flags().GetInt("port")
			oneshot, _ := cmd.Flags().GetBool("oneshot")

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			res, err := daemon.Open(ctx, ws)
			if err != nil {
				return err
			}
			defer func() { _ = res.Close() }()

			svc := jsonrpc.NewService(ws, res.Log, res.Code, res.Queue, res.Registry, res.Overlay)
			srv, err := studio.NewServer(svc)
			if err != nil {
				return err
			}
			ln, err := srv.Listen(port)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "Studio: %s\n", srv.URL(ln.Addr().String()))
			_, _ = fmt.Fprintln(out, "Bound to loopback; Origin must match http://127.0.0.1:* or http://localhost:*")
			if oneshot {
				_ = ln.Close()
				return nil
			}
			return srv.Serve(ctx, ln)
		},
	}
}
