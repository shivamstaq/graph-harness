package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/review_queue"
)

// openReviewQueue colocates the review queue store with the event log under
// the runtime dir. Kept as a fallback for cases where the daemon is not
// running and the command is intrinsically read-only (list/get).
func openReviewQueue(ws *daemon.Workspace) (*review_queue.Queue, *sql.DB, error) {
	dsn := ws.EventLog + ".review.queue?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	q, err := review_queue.NewQueue(db)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	// Auto-load per-proposal-kind evidence requirements (SPEC §10.4) declared
	// across embedded layer manifests. Call ordering is name-sorted; Queue
	// merges definitions across manifests.
	if err := kernel.WalkEmbeddedManifests(func(name string, data []byte) error {
		if loadErr := q.LoadRequirements(data); loadErr != nil {
			return fmt.Errorf("load evidence requirements from %s: %w",
				strings.TrimSuffix(name, ".yaml"), loadErr)
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return q, db, nil
}

// newReviewCmdReal replaces the stub. P0.T40 + P1.5.T06 (daemon
// routing): list/get prefer the daemon when running and fall back
// to read-only SQLite when not. accept/reject/submit/add-evidence
// auto-EnsureRunning the daemon since they journal writes through
// the single-writer path (SPEC §9.1).
func newReviewCmdReal() *cobra.Command {
	c := &cobra.Command{Use: "review", Short: "Inspect and resolve the review queue"}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List pending review items",
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
			out := cmd.OutOrStdout()
			asJSON, _ := cmd.Flags().GetBool("json")
			var items []review_queue.Proposal
			if handle.Mode == ModeDaemon {
				var res jsonrpc.ReviewListResult
				if err := handle.Client.Call(ctx, "review.list", jsonrpc.ReviewListParams{}, &res); err != nil {
					return err
				}
				items = res.Items
			} else {
				q, db, err := openReviewQueue(ws)
				if err != nil {
					return err
				}
				defer func() { _ = db.Close() }()
				items, err = q.List(ctx, "")
				if err != nil {
					return err
				}
			}
			if asJSON {
				return json.NewEncoder(out).Encode(items)
			}
			if len(items) == 0 {
				_, _ = fmt.Fprintln(out, "(no proposals in the review queue)")
				return nil
			}
			for _, p := range items {
				_, _ = fmt.Fprintf(out, "%-12s %-20s %-10s %s\n", p.ID, p.TargetLayer, p.State, p.Kind)
			}
			return nil
		},
	}
	listCmd.Flags().Bool("json", false, "emit JSON")

	getCmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Show one review item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			var p *review_queue.Proposal
			if handle.Mode == ModeDaemon {
				if err := handle.Client.Call(ctx, "review.get",
					jsonrpc.ReviewIDParams{ID: args[0]}, &p); err != nil {
					return err
				}
			} else {
				q, db, err := openReviewQueue(ws)
				if err != nil {
					return err
				}
				defer func() { _ = db.Close() }()
				p, err = q.Get(ctx, args[0])
				if err != nil {
					return err
				}
			}
			if p == nil {
				return fmt.Errorf("proposal %s not found", args[0])
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(p)
		},
	}

	acceptCmd := &cobra.Command{
		Use:   "accept <id>",
		Short: "Accept a proposal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			handle, err := ResolveRoute(ctx, ws, RouteOptions{RequireWriter: true})
			if err != nil {
				return err
			}
			defer func() { _ = handle.Close() }()
			var ack jsonrpc.ReviewAck
			if err := handle.Client.Call(ctx, "review.accept",
				jsonrpc.ReviewIDParams{ID: args[0]}, &ack); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "✓ accepted %s\n", args[0])
			return nil
		},
	}

	rejectCmd := &cobra.Command{
		Use:   "reject <id>",
		Short: "Reject a proposal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			handle, err := ResolveRoute(ctx, ws, RouteOptions{RequireWriter: true})
			if err != nil {
				return err
			}
			defer func() { _ = handle.Close() }()
			var ack jsonrpc.ReviewAck
			if err := handle.Client.Call(ctx, "review.reject",
				jsonrpc.ReviewIDParams{ID: args[0]}, &ack); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "✓ rejected %s\n", args[0])
			return nil
		},
	}

	submitCmd := &cobra.Command{
		Use:   "submit",
		Short: "Submit a proposal (test-only convenience; agents go through MCP)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			handle, err := ResolveRoute(ctx, ws, RouteOptions{RequireWriter: true})
			if err != nil {
				return err
			}
			defer func() { _ = handle.Close() }()
			layer, _ := cmd.Flags().GetString("layer")
			kind, _ := cmd.Flags().GetString("kind")
			author, _ := cmd.Flags().GetString("author")
			payload, _ := cmd.Flags().GetString("payload")
			description, _ := cmd.Flags().GetString("description")
			evidenceJSON, _ := cmd.Flags().GetString("evidence")
			var evidence []review_queue.EvidenceItem
			if strings.TrimSpace(evidenceJSON) != "" {
				if err := json.Unmarshal([]byte(evidenceJSON), &evidence); err != nil {
					return fmt.Errorf("--evidence: %w", err)
				}
			}
			var res jsonrpc.ReviewSubmitResult
			if err := handle.Client.Call(ctx, "review.submit", jsonrpc.ReviewSubmitParams{
				TargetLayer: layer, Kind: kind, Author: author,
				Description: description,
				Payload:     json.RawMessage(payload),
				Evidence:    evidence,
			}, &res); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintln(out, res.ID)
			_, _ = fmt.Fprintf(out, "state: %s\n", res.State)
			if res.Item != nil && res.Item.State == review_queue.StateNeedsEvidence {
				for _, m := range res.Item.MissingItems {
					_, _ = fmt.Fprintf(out, "  missing: %s\n", m)
				}
			}
			return nil
		},
	}
	submitCmd.Flags().String("layer", "semantic.overlay", "target layer")
	submitCmd.Flags().String("kind", "selector", "proposal kind")
	submitCmd.Flags().String("author", "agent:test", "author tag")
	submitCmd.Flags().String("payload", "", "JSON payload")
	submitCmd.Flags().String("description", "", "human-readable proposal description")
	submitCmd.Flags().String("evidence", "", "evidence as JSON array, e.g. '[{\"kind\":\"symbol\",\"detail\":\"X.Y\"}]'")

	addEvidenceCmd := &cobra.Command{
		Use:   "add-evidence <id>",
		Short: "Attach evidence to a needs_evidence proposal; auto-promotes when complete",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			handle, err := ResolveRoute(ctx, ws, RouteOptions{RequireWriter: true})
			if err != nil {
				return err
			}
			defer func() { _ = handle.Close() }()
			evidenceJSON, _ := cmd.Flags().GetString("evidence")
			var evidence []review_queue.EvidenceItem
			if err := json.Unmarshal([]byte(evidenceJSON), &evidence); err != nil {
				return fmt.Errorf("--evidence: %w", err)
			}
			var res jsonrpc.ReviewAddEvidenceResult
			if err := handle.Client.Call(ctx, "review.addEvidence",
				jsonrpc.ReviewAddEvidenceParams{ID: args[0], Evidence: evidence}, &res); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "state: %s\n", res.State)
			return nil
		},
	}
	addEvidenceCmd.Flags().String("evidence", "[]", "evidence items as JSON array")

	c.AddCommand(listCmd, getCmd, acceptCmd, rejectCmd, submitCmd, addEvidenceCmd)
	_ = context.Background
	return c
}
