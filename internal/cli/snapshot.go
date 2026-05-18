package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// newSnapshotCmd implements `graph-harness snapshot {create,list,restore}`
// per F12 / P0.T11. All three subcommands route through the daemon —
// snapshots are kernel-canonical writes, and `restore` in particular
// is a heavy operation that MUST go through the single-writer path.
//
// Subcommand semantics:
//
//   - snapshot create [--layer X] [--seq N]: capture a snapshot at seq
//     (or current head when N=0); print the SnapshotHandle as JSON.
//   - snapshot list [--layer X]: enumerate captured snapshots.
//   - snapshot restore <id>: replay the snapshot's events back into
//     the live log. Pauses the writer; do not invoke during active
//     editing.
func newSnapshotCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "snapshot",
		Short: "Capture, list, and restore kernel event-log snapshots (P0.T11)",
	}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Capture a snapshot at the current head (or --seq) for [--layer]",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			layer, _ := cmd.Flags().GetString("layer")
			seq, _ := cmd.Flags().GetUint64("seq")

			ctx := cmd.Context()
			handle, err := ResolveRoute(ctx, ws, RouteOptions{RequireWriter: true})
			if err != nil {
				return err
			}
			defer func() { _ = handle.Close() }()

			var snap kernel.SnapshotHandle
			if err := handle.Client.Call(ctx, "kernel.snapshot",
				jsonrpc.SnapshotCreateParams{Layer: layer, Seq: seq}, &snap); err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(snap)
		},
	}
	createCmd.Flags().String("layer", "", "scope snapshot to one layer; empty = every layer")
	createCmd.Flags().Uint64("seq", 0, "snapshot at this seq; 0 = current head")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List captured snapshots (newest first)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			layer, _ := cmd.Flags().GetString("layer")
			asJSON, _ := cmd.Flags().GetBool("json")

			ctx := cmd.Context()
			h, err := ResolveRoute(ctx, ws, RouteOptions{})
			if err != nil {
				return err
			}
			defer func() { _ = h.Close() }()

			var res jsonrpc.SnapshotListResult
			if h.Mode == ModeDaemon {
				if err := h.Client.Call(ctx, "kernel.listSnapshots",
					jsonrpc.SnapshotListParams{Layer: layer}, &res); err != nil {
					return err
				}
			} else {
				// Batch fallback — daemon not running; tail the
				// table directly via the batch handle's read-only
				// EventLog. The batch surface doesn't expose the
				// snapshots query, so for the daemon-unavailable
				// path we just emit the empty list and a notice.
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "(daemon not running; snapshot list returns empty in batch mode)")
				res = jsonrpc.SnapshotListResult{Snapshots: []jsonrpc.SnapshotInfo{}}
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
			}
			if len(res.Snapshots) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "(no snapshots captured)")
				return nil
			}
			for _, info := range res.Snapshots {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%-16s seq=%-8d layer=%-20s %s\n",
					info.ID, info.Seq, info.Layer, info.CreatedAt)
			}
			return nil
		},
	}
	listCmd.Flags().String("layer", "", "filter to one layer; empty = every layer")
	listCmd.Flags().Bool("json", false, "emit JSON")

	restoreCmd := &cobra.Command{
		Use:   "restore <id>",
		Short: "Replay a snapshot's events back into the live log (heavy operation)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			h, err := ResolveRoute(ctx, ws, RouteOptions{RequireWriter: true})
			if err != nil {
				return err
			}
			defer func() { _ = h.Close() }()

			var res jsonrpc.SnapshotRestoreResult
			if err := h.Client.Call(ctx, "kernel.restoreSnapshot",
				jsonrpc.SnapshotRestoreParams{ID: args[0]}, &res); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "restored %d events from snapshot %s\n",
				res.EventsRestored, args[0])
			return nil
		},
	}

	c.AddCommand(createCmd, listCmd, restoreCmd)
	return c
}
