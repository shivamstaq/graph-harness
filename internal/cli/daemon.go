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
		newDaemonPublishTransientCmd(),
		newDaemonGetTransientCmd(),
		newDaemonAuditUntaggedCmd(),
		newDaemonRebuildFromSnapshotCmd(),
	)
	return c
}

// newDaemonPublishTransientCmd implements `graph-harness daemon
// publish-transient` — the CLI surface for SPEC §6.19 in-memory
// transient publishes. Opens a long-lived JSON-RPC connection,
// optionally identifies with a stable subscriber id, calls
// kernel.publishTransient, and blocks until SIGINT/SIGTERM so the
// connection stays open (transient entries are scoped to the
// publishing connection; closing it drops them).
//
// Test surface for P1.5.T09 — the e2e spec
// kernel/transient-overlay-disconnect-drops-state.yaml exercises
// the publish → get → disconnect-drop round trip.
func newDaemonPublishTransientCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "publish-transient",
		Short: "Publish a transient overlay entry and hold the connection (SPEC §6.19)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			sourceClass, _ := cmd.Flags().GetString("source-class")
			targetID, _ := cmd.Flags().GetString("target-id")
			payloadStr, _ := cmd.Flags().GetString("payload")
			subID, _ := cmd.Flags().GetString("subscriber-id")
			holdFor, _ := cmd.Flags().GetDuration("hold")

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			if holdFor > 0 {
				ctx, cancel = context.WithTimeout(ctx, holdFor)
				defer cancel()
			}
			if err := daemon.EnsureRunning(ctx, ws); err != nil {
				return err
			}
			client, err := jsonrpc.Dial(ctx, ws.SocketPath)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()

			if subID != "" {
				var ack jsonrpc.IdentifyResult
				if err := client.Call(ctx, "kernel.identify",
					jsonrpc.IdentifyParams{SubscriberID: subID}, &ack); err != nil {
					return fmt.Errorf("identify: %w", err)
				}
			}
			payload := json.RawMessage([]byte(payloadStr))
			if len(payload) == 0 {
				payload = json.RawMessage([]byte("null"))
			}
			var res jsonrpc.PublishTransientResult
			if err := client.Call(ctx, "kernel.publishTransient",
				jsonrpc.PublishTransientParams{
					SourceClass: sourceClass,
					TargetID:    targetID,
					Payload:     payload,
				}, &res); err != nil {
				return fmt.Errorf("publishTransient: %w", err)
			}
			ack := map[string]any{
				"method": "transient.published",
				"params": map[string]any{
					"subscriber_id": res.SubscriberID,
					"target_id":     targetID,
					"source_class":  sourceClass,
				},
			}
			buf, _ := json.Marshal(ack)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(buf))

			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
			defer signal.Stop(sig)
			select {
			case <-sig:
				return nil
			case <-ctx.Done():
				if ctx.Err() == context.DeadlineExceeded {
					return nil
				}
				return ctx.Err()
			}
		},
	}
	c.Flags().String("source-class", "editor", "transient source class (editor|agent_draft|cli_form|studio_form|team_pending)")
	c.Flags().String("target-id", "", "target entity id this transient claim attaches to")
	c.Flags().String("payload", "", "JSON payload body for the transient entry")
	c.Flags().String("subscriber-id", "", "stable subscriber id; transient entries are scoped to this conn")
	c.Flags().Duration("hold", 0, "automatically disconnect after this duration (0 = wait for signal)")
	return c
}

// newDaemonRebuildFromSnapshotCmd implements `graph-harness daemon
// rebuild-from-snapshot --subscription-id <id>` — a thin diagnostic
// surface over the F1 / SPEC §6.22 recovery RPC. Emits the result
// envelope (CheckpointID, CheckpointSeq, NewCursor, RecoveryHint)
// as one JSON line. Used by the F1 e2e to validate the wire shape
// after a subscriber overflow.
func newDaemonRebuildFromSnapshotCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "rebuild-from-snapshot",
		Short: "Advance a subscription's cursor to current head (SPEC §6.22 recovery; F1)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			subID, _ := cmd.Flags().GetString("subscription-id")
			if subID == "" {
				return fmt.Errorf("--subscription-id required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			if err := daemon.EnsureRunning(ctx, ws); err != nil {
				return err
			}
			client, err := jsonrpc.Dial(ctx, ws.SocketPath)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			var res jsonrpc.RebuildFromSnapshotResult
			if err := client.Call(ctx, "kernel.rebuildFromSnapshot",
				jsonrpc.RebuildFromSnapshotParams{SubscriptionID: subID}, &res); err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
		},
	}
	c.Flags().String("subscription-id", "", "subscription_id returned by kernel.subscribe")
	return c
}

// newDaemonAuditUntaggedCmd implements `graph-harness daemon
// audit-untagged` per F16 / P1.5.T07. Routes through the daemon's
// AuditUntagged RPC (or falls back to opening the local Store
// read-only when no daemon is running) and prints any rows whose
// kernel_seq_tag is zero — the writer-monopoly violation signal.
//
// Exits non-zero when offenders exist so CI smoke tests can use
// the command directly as a gate.
func newDaemonAuditUntaggedCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "audit-untagged",
		Short: "Audit code.core for rows lacking a kernel-issued seq tag (F16)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			h, err := ResolveRoute(ctx, ws, RouteOptions{})
			if err != nil {
				return err
			}
			defer func() { _ = h.Close() }()

			var res jsonrpc.AuditUntaggedResult
			if h.Mode != ModeDaemon {
				return fmt.Errorf("audit-untagged requires daemon (the writer-monopoly contract is only enforceable through the daemon)")
			}
			if err := h.Client.Call(ctx, "daemon.auditUntagged", nil, &res); err != nil {
				return err
			}
			if asJSON {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(res); err != nil {
					return err
				}
			} else {
				out := cmd.OutOrStdout()
				_, _ = fmt.Fprintf(out, "entities:   %d\n", len(res.Entities))
				_, _ = fmt.Fprintf(out, "provenance: %d\n", len(res.Provenance))
				_, _ = fmt.Fprintf(out, "relations:  %d\n", len(res.Relations))
				if res.TotalCount > 0 {
					_, _ = fmt.Fprintln(out, "---")
					for _, id := range res.Entities {
						_, _ = fmt.Fprintf(out, "entity:    %s\n", id)
					}
					for _, p := range res.Provenance {
						_, _ = fmt.Fprintf(out, "provenance:%s\n", p)
					}
					for _, r := range res.Relations {
						_, _ = fmt.Fprintf(out, "relation:  %s\n", r)
					}
				} else {
					_, _ = fmt.Fprintln(out, "audit clean (writer-monopoly holds)")
				}
			}
			if res.TotalCount > 0 {
				return fmt.Errorf("audit found %d untagged rows (writer-monopoly violation)", res.TotalCount)
			}
			return nil
		},
	}
	c.Flags().Bool("json", false, "emit JSON")
	return c
}

// newDaemonGetTransientCmd implements `graph-harness daemon
// get-transient --target-id <id>` — the diagnostic read surface for
// the in-memory transient tier (SPEC §6.19). Returns the most-recent
// transient entry for target_id across all subscribers as one NDJSON
// line. Exits 0 when an entry exists, 1 when none does.
func newDaemonGetTransientCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "get-transient",
		Short: "Read a transient overlay entry by target_id (SPEC §6.19)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			targetID, _ := cmd.Flags().GetString("target-id")
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			if err := daemon.EnsureRunning(ctx, ws); err != nil {
				return err
			}
			client, err := jsonrpc.Dial(ctx, ws.SocketPath)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			var res jsonrpc.GetTransientResult
			if err := client.Call(ctx, "kernel.getTransient",
				jsonrpc.GetTransientParams{TargetID: targetID}, &res); err != nil {
				return fmt.Errorf("getTransient: %w", err)
			}
			buf, _ := json.Marshal(res)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(buf))
			if !res.Found {
				return fmt.Errorf("no transient entry for target_id=%s", targetID)
			}
			return nil
		},
	}
	c.Flags().String("target-id", "", "target entity id to read")
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
			subName, _ := cmd.Flags().GetString("subscription-name")
			cursor, _ := cmd.Flags().GetUint64("cursor")
			maxEvents, _ := cmd.Flags().GetInt("max-events")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			queueCap, _ := cmd.Flags().GetInt("queue-cap")
			pingInterval, _ := cmd.Flags().GetDuration("ping-interval")

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
				jsonrpc.SubscribeParams{Filter: filter, Cursor: cursor, QueueCap: queueCap, Name: subName},
				&subscribed); err != nil {
				return fmt.Errorf("subscribe: %w", err)
			}
			meta, _ := json.Marshal(map[string]any{
				"method": "subscription.bound",
				"params": subscribed,
			})
			_, _ = fmt.Fprintln(out, string(meta))

			// Optional heartbeat: F17 — when the daemon's
			// eviction threshold is tight (CI specs use ~500ms),
			// the subscriber needs to call kernel.ping on an
			// interval to keep its conn marked-live. Production
			// long-lived consumers (TUI, IDE) wire this on their
			// own idle timer; the CLI surfaces it via --ping-interval
			// so e2e specs can write observers that survive eviction.
			var pingTickC <-chan time.Time
			if pingInterval > 0 {
				pt := time.NewTicker(pingInterval)
				defer pt.Stop()
				pingTickC = pt.C
			}

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
					if autoAck && ev.SubscriptionID != "" {
						_ = client.Call(ctx, "kernel.ack",
							jsonrpc.AckParams{SubscriptionID: ev.SubscriptionID, Seq: ev.Seq},
							nil)
					}
				case <-pingTickC:
					_ = client.Call(ctx, "kernel.ping", nil, nil)
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
	c.Flags().String("subscription-name", "", "subscription_name persisted alongside subscriber-id (F8); defaults to the filter expression")
	c.Flags().Uint64("cursor", 0, "resume from cursor+1; zero starts at current head")
	c.Flags().Int("max-events", 1, "exit after receiving N kernel.event notifications (0 = run forever)")
	c.Flags().Duration("timeout", 5*time.Second, "exit after this duration; zero means no timeout")
	c.Flags().Int("queue-cap", 0, "override per-subscription queue depth (default 10000)")
	c.Flags().Bool("auto-ack", false, "send kernel.ack for each received event (persists the cursor in layer_state)")
	c.Flags().Duration("ping-interval", 0, "send kernel.ping every duration to keep the conn alive past eviction threshold (F17)")
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
			evictThreshold, _ := cmd.Flags().GetDuration("eviction-threshold")
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

			// SPEC §6.22 heartbeat eviction: the SubscriptionManager
			// sweeps idle conns past the configured threshold and
			// emits SubscriberEvicted on the kernel bus. F17:
			// --eviction-threshold overrides the default (24h);
			// zero/unset keeps the default. Tests pass small values
			// (e.g. 500ms) to make the sweep observable in CI time.
			if evictThreshold > 0 {
				svc.SetSubscriptionIdleThreshold(evictThreshold)
			}
			svc.StartSubscriptionEviction(ctx)

			// F10 / P0.T06: retention sweep. Compacts event-log rows
			// strictly below each layer's Retention.Events cutoff,
			// using kernel.CreateSnapshot to satisfy the recoverability
			// proof Compact requires. Errors are logged but non-fatal.
			go daemon.RetentionLoop(ctx, res.Log, res.Registry, 0, func(format string, args ...any) {
				fmt.Fprintf(cmd.ErrOrStderr(), "[retention] "+format+"\n", args...)
			})

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
	c.Flags().Duration("eviction-threshold", 0, "subscription idle eviction threshold (0 = SubscriptionManager default; F17)")
	return c
}
