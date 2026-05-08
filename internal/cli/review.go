package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/review_queue"
)

// openReviewQueue colocates the review queue store with the event log under
// the runtime dir.
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

// newReviewCmdReal replaces the stub. P0.T40.
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
			q, db, err := openReviewQueue(ws)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			items, err := q.List(context.Background(), "")
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(items)
			}
			out := cmd.OutOrStdout()
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
			q, db, err := openReviewQueue(ws)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			p, err := q.Get(context.Background(), args[0])
			if err != nil {
				return err
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
			q, db, err := openReviewQueue(ws)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			if err := q.Accept(context.Background(), args[0]); err != nil {
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
			q, db, err := openReviewQueue(ws)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			if err := q.Reject(context.Background(), args[0]); err != nil {
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
			q, db, err := openReviewQueue(ws)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
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
			id, err := q.Submit(context.Background(), review_queue.Proposal{
				TargetLayer: layer, Kind: kind, Author: author, Payload: []byte(payload),
				Description: description, Evidence: evidence,
			})
			if err != nil {
				return err
			}
			// Echo back the resulting state so submitters see needs_evidence
			// when their evidence falls short of the manifest requirements.
			p, _ := q.Get(context.Background(), id)
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintln(out, id)
			if p != nil {
				_, _ = fmt.Fprintf(out, "state: %s\n", p.State)
				if p.State == review_queue.StateNeedsEvidence {
					for _, m := range p.MissingItems {
						_, _ = fmt.Fprintf(out, "  missing: %s\n", m)
					}
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
			q, db, err := openReviewQueue(ws)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			evidenceJSON, _ := cmd.Flags().GetString("evidence")
			var evidence []review_queue.EvidenceItem
			if err := json.Unmarshal([]byte(evidenceJSON), &evidence); err != nil {
				return fmt.Errorf("--evidence: %w", err)
			}
			st, err := q.AddEvidence(context.Background(), args[0], evidence)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "state: %s\n", st)
			return nil
		},
	}
	addEvidenceCmd.Flags().String("evidence", "[]", "evidence items as JSON array")

	c.AddCommand(listCmd, getCmd, acceptCmd, rejectCmd, submitCmd, addEvidenceCmd)
	return c
}
