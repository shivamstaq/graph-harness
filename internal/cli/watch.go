package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
)

// newWatchCmd implements `graph-harness watch [filter]` — a top-level
// shortcut for `daemon subscribe --filter <filter> --max-events 0
// --auto-ack`. Per F2 + plan/answers/07 P0.T19a, watch is the
// canonical surface for "tail the kernel bus until I cancel."
//
// Routing: the daemon is auto-spawned (writer-monopoly subscribers
// need the daemon to be the seq source-of-truth). The filter accepts
// the full Participle grammar (F20) or layer / layer-kind shorthand.
func newWatchCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "watch [filter]",
		Short: "Tail kernel events live (subscribe to the daemon bus)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			filter := ""
			if len(args) > 0 {
				filter = args[0]
			}
			if f, _ := cmd.Flags().GetString("filter"); f != "" {
				filter = f
			}
			maxEvents, _ := cmd.Flags().GetInt("max-events")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			subscriberID, _ := cmd.Flags().GetString("subscriber-id")
			subName, _ := cmd.Flags().GetString("subscription-name")

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

			if subscriberID != "" {
				var id jsonrpc.IdentifyResult
				if err := client.Call(ctx, "kernel.identify",
					jsonrpc.IdentifyParams{SubscriberID: subscriberID}, &id); err != nil {
					return fmt.Errorf("identify: %w", err)
				}
			}
			var subscribed jsonrpc.SubscribeResult
			if err := client.Call(ctx, "kernel.subscribe",
				jsonrpc.SubscribeParams{Filter: filter, Name: subName},
				&subscribed); err != nil {
				return fmt.Errorf("subscribe: %w", err)
			}
			meta, _ := json.Marshal(map[string]any{
				"method": "subscription.bound",
				"params": subscribed,
			})
			_, _ = fmt.Fprintln(out, string(meta))

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
					if ev.SubscriptionID != "" {
						_ = client.Call(ctx, "kernel.ack",
							jsonrpc.AckParams{SubscriptionID: ev.SubscriptionID, Seq: ev.Seq}, nil)
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
	c.Flags().String("filter", "", "event filter (Participle grammar or shorthand; overrides positional arg)")
	c.Flags().Int("max-events", 0, "exit after N kernel events (0 = run until signal)")
	c.Flags().Duration("timeout", 0, "exit after this duration (0 = no timeout)")
	c.Flags().String("subscriber-id", "", "stable subscriber id; persists cursor across reconnect")
	c.Flags().String("subscription-name", "", "subscription_name persisted alongside subscriber-id (F8)")
	return c
}
