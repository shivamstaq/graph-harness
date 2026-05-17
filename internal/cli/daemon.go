package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/extract"
	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
)

// newDaemonCmdReal wires daemon {start, stop, status, logs, serve}.
//
// `daemon serve` is the in-process loop that holds the JSON-RPC listener;
// other surfaces (CLI status, TUI, Studio, MCP, VS Code) talk to it through
// the workspace socket. `daemon start` is a thin convenience that spawns
// the serve loop as a child and returns once the socket is connectable.
func newDaemonCmdReal() *cobra.Command {
	c := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the workspace daemon (lazy-spawn lifecycle)",
	}
	c.AddCommand(
		newDaemonStartCmd(),
		newDaemonStopCmd(),
		newDaemonStatusCmd(),
		newDaemonLogsCmd(),
		newDaemonServeCmd(),
	)
	return c
}

func newDaemonStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the daemon (idempotent; lazy-spawn)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			if err := daemon.EnsureRunning(ctx, ws); err != nil {
				return err
			}
			st := daemon.Inspect(ws)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "✓ daemon running (pid=%d) socket=%s\n", st.PID, st.SocketPath)
			return nil
		},
	}
}

func newDaemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon (best-effort; SIGTERM after 5s)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			if err := daemon.Stop(ctx, ws); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "✓ daemon stopped")
			return nil
		},
	}
}

func newDaemonStatusCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: "Daemon status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			st := daemon.Inspect(ws)
			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(st)
			}
			out := cmd.OutOrStdout()
			if !st.Running {
				_, _ = fmt.Fprintln(out, "daemon: not running")
				return nil
			}
			_, _ = fmt.Fprintf(out, "daemon: running (pid=%d) since %s\n", st.PID, st.StartedAt.Format(time.RFC3339))
			_, _ = fmt.Fprintf(out, "  socket:  %s\n", st.SocketPath)
			_, _ = fmt.Fprintf(out, "  pidfile: %s\n", st.PidFilePath)
			return nil
		},
	}
	c.Flags().Bool("json", false, "emit JSON")
	return c
}

func newDaemonLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs",
		Short: "Tail daemon logs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			path := ws.EventLog + ".daemon.log"
			data, err := os.ReadFile(path) // #nosec G304 -- under workspace runtime dir
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "(no daemon log yet)")
					return nil
				}
				return err
			}
			_, _ = cmd.OutOrStdout().Write(data)
			return nil
		},
	}
}

// newDaemonServeCmd is the in-process listener loop.
func newDaemonServeCmd() *cobra.Command {
	c := &cobra.Command{
		Use:    "serve",
		Short:  "(internal) run the daemon listener loop",
		Hidden: false, // visible — operators may want it for `--no-daemon`-like flows
		RunE: func(cmd *cobra.Command, _ []string) error {
			wsPath, _ := cmd.Flags().GetString("workspace")
			idle, _ := cmd.Flags().GetDuration("idle-timeout")
			var ws *daemon.Workspace
			if wsPath != "" {
				w, err := daemon.From(wsPath)
				if err != nil {
					return err
				}
				ws = w
			} else {
				w, err := daemon.Discover()
				if err != nil {
					return err
				}
				ws = w
			}
			if !ws.IsInitialized() {
				return fmt.Errorf("workspace at %s is not initialized; run `graph-harness init`", ws.Root)
			}
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			// Catch SIGINT/SIGTERM so deferred cleanup runs.
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sig
				cancel()
			}()

			// Daemon mode: open with the always-on watcher loop enabled
			// (SPEC §6.20). The orchestrator stays resident; fsnotify
			// events route through it for the daemon's lifetime.
			res, err := daemon.OpenWithOptions(ctx, ws, daemon.OpenOptions{
				EnableWatcher: true,
				ExtractOptions: extract.Options{
					DisableLSP:  lspDisabledFromEnv(),
					DisableSCIP: false,
				},
				ErrLog: func(format string, args ...any) {
					// Daemon logs ride the cobra root's stderr writer so
					// `daemon start --foreground` surfaces watcher hiccups.
					fmt.Fprintf(cmd.ErrOrStderr(), "[watch] "+format+"\n", args...)
				},
			})
			if err != nil {
				return fmt.Errorf("open resources: %w", err)
			}
			defer func() { _ = res.Close() }()

			ln, err := daemon.Listen(ws)
			if err != nil {
				return err
			}
			defer func() { _ = ln.Close() }()
			defer daemon.CleanupOnExit(ws)

			svc := jsonrpc.NewService(ws, res.Log, res.Code, res.Queue, res.Registry, res.Overlay)
			srv := jsonrpc.NewServer(svc)

			// idle-timeout watcher
			go daemon.IdleWatcher(ctx, daemon.Options{IdleTimeout: idle, Logger: nil}, svc.LastActive, cancel)

			// daemon.shutdown RPC → cancel listener loop
			go func() {
				select {
				case <-svc.ShutdownC():
					cancel()
				case <-ctx.Done():
				}
			}()

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "graph-harness daemon serving on %s (pid=%d, idle-timeout=%s)\n", ws.SocketPath, os.Getpid(), idle)
			return srv.Serve(ctx, ln)
		},
	}
	c.Flags().String("workspace", "", "workspace root (default: discover from cwd)")
	c.Flags().Duration("idle-timeout", daemon.DefaultIdleTimeout, "exit after N of inactivity (0 = never)")
	return c
}
