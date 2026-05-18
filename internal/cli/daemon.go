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
		newDaemonSubscribeCmd(),
	)
	return c
}

// newDaemonSubscribeCmd implements `graph-harness daemon subscribe
// --filter <expr>`. Establishes a long-lived JSON-RPC connection to
// the daemon, calls kernel.identify (binding a stable subscriber_id
// when --subscriber-id is set) + kernel.subscribe, then prints every
// pushed notification to stdout as a single NDJSON line.
//
// Demo/test surface for the P0.5 substrate contract (SPEC §6.22).
// The TUI / Studio / IDE long-lived consumers in P3+ use the same
// `kernel.subscribe` method via their own RPC clients.
//
// Exits on:
//   - --max-events N notifications received
//   - --timeout elapsed
//   - SIGINT / SIGTERM
//   - daemon disconnect
func newDaemonSubscribeCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "subscribe",
		Short: "Stream kernel events from the daemon over JSON-RPC (SPEC §6.22)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			filter, _ := cmd.Flags().GetString("filter")
			subID, _ := cmd.Flags().GetString("subscriber-id")
			cursor, _ := cmd.Flags().GetUint64("cursor")
			maxEvents, _ := cmd.Flags().GetInt("max-events")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			queueCap, _ := cmd.Flags().GetInt("queue-cap")

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			if timeout > 0 {
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}

			if err := daemon.EnsureRunning(ctx, ws); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			autoAck, _ := cmd.Flags().GetBool("auto-ack")
			type seqEvent struct {
				Seq            uint64 `json:"seq"`
				SubscriptionID string `json:"subscription_id"`
			}
			eventC := make(chan seqEvent, maxEvents+8)
			handler := func(method string, params json.RawMessage) {
				row := map[string]any{"method": method}
				if len(params) > 0 {
					row["params"] = params
				}
				buf, _ := json.Marshal(row)
				_, _ = fmt.Fprintln(out, string(buf))
				if method == "kernel.event" {
					var e seqEvent
					_ = json.Unmarshal(params, &e)
					eventC <- e
				}
			}
			client, err := jsonrpc.DialWithHandler(ctx, ws.SocketPath, handler)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()

			if subID != "" {
				var id jsonrpc.IdentifyResult
				if err := client.Call(ctx, "kernel.identify",
					jsonrpc.IdentifyParams{SubscriberID: subID}, &id); err != nil {
					return fmt.Errorf("identify: %w", err)
				}
			}
			var subscribed jsonrpc.SubscribeResult
			if err := client.Call(ctx, "kernel.subscribe",
				jsonrpc.SubscribeParams{Filter: filter, Cursor: cursor, QueueCap: queueCap},
				&subscribed); err != nil {
				return fmt.Errorf("subscribe: %w", err)
			}
			meta, _ := json.Marshal(map[string]any{
				"method": "subscription.bound",
				"params": subscribed,
			})
			_, _ = fmt.Fprintln(out, string(meta))

			// Block until max-events received, timeout, or signal.
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
			defer signal.Stop(sig)
			seen := 0
			for {
				if maxEvents > 0 && seen >= maxEvents {
					return nil
				}
				select {
				case ev := <-eventC:
					seen++
					// SPEC §6.22 + P0.5.T02/T03 cross-restart
					// persistence: each received event is ack'd
					// when --auto-ack is set. The daemon's Ack
					// handler persists last_processed_seq into
					// layer_state via EventLog.AdvanceCursor so a
					// subsequent reconnect with the same
					// subscriber_id resumes from seq+1 instead of
					// head. Without --auto-ack the cursor stays
					// in-process only and is dropped on disconnect.
					if autoAck && ev.SubscriptionID != "" {
						_ = client.Call(ctx, "kernel.ack",
							jsonrpc.AckParams{SubscriptionID: ev.SubscriptionID, Seq: ev.Seq},
							nil)
					}
				case <-sig:
					return nil
				case <-ctx.Done():
					if ctx.Err() == context.DeadlineExceeded {
						return nil
					}
					return ctx.Err()
				}
			}
		},
	}
	c.Flags().String("filter", "", "Participle event filter (e.g. 'code.core' or 'code.core/FileChanged')")
	c.Flags().String("subscriber-id", "", "stable subscriber identifier; persists cursors across reconnect")
	c.Flags().Uint64("cursor", 0, "resume from cursor+1; zero starts at current head")
	c.Flags().Int("max-events", 1, "exit after receiving N kernel.event notifications (0 = run forever)")
	c.Flags().Duration("timeout", 5*time.Second, "exit after this duration; zero means no timeout")
	c.Flags().Int("queue-cap", 0, "override per-subscription queue depth (default 10000)")
	c.Flags().Bool("auto-ack", false, "send kernel.ack for each received event (persists the cursor in layer_state)")
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
