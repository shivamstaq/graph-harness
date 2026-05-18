package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/change_process"
	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/semantic_overlay"
)

// newSelectorsTestCmd implements `graph-harness selectors test <name>`
// (P0.T27 + P1.T29: --explain prints the full multi-anchor ladder).
func newSelectorsTestCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "test <name>",
		Short: "Resolve a named selector and print its outcome envelope",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			ctx := context.Background()
			batch, _ := cmd.Flags().GetBool("batch")

			// Daemon path: lower through Kernel.Route via JSON-RPC.
			// Batch path: open layer SQLite read-only locally,
			// re-extract (so the index is current), and call the
			// in-process resolver. Both paths produce the same
			// ResolutionEnvelope shape (P0.5.T17).
			if !batch {
				return runSelectorsTestDaemon(ctx, cmd, ws, args[0])
			}
			return runSelectorsTestBatch(ctx, cmd, ws, args[0])
		},
	}
	c.Flags().Bool("json", false, "emit envelope as JSON")
	c.Flags().Bool("explain", false, "print the full multi-anchor ladder evaluation")
	addBatchFlag(c)
	addExtractorToggleFlags(c)
	return c
}

// runSelectorsTestBatch implements `selectors test --batch` —
// daemon-free, read-only-SQLite resolution. The batch path still
// runs the orchestrator's incremental index so newly edited files
// are reflected; an --batch alone without a fresh index would surface
// stale data and diverge from the daemon path.
func runSelectorsTestBatch(ctx context.Context, cmd *cobra.Command, ws *daemon.Workspace, name string) error {
	// Index runs against a writable store so the orchestrator can
	// upsert into code.core; the batch read after the index uses
	// the just-written rows (single-process; no daemon contention).
	log, err := facts.OpenEventLog(ws.EventLog)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	store, db, err := openCodeStore(ws)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err := indexWorkspaceCodeWithOptions(ctx, ws, store, log, extractOptionsFromFlags(cmd)); err != nil {
		return err
	}
	overlay, err := loadOverlay(ws)
	if err != nil {
		return err
	}
	env, trace, err := overlay.ResolveWithTrace(ctx, name, store, log.LastSeq())
	if err != nil {
		return err
	}
	return renderSelectorsTest(cmd, env, trace)
}

// runSelectorsTestDaemon proxies through the daemon's JSON-RPC
// selectors.test method (which lowers through Kernel.Route). When
// --explain is set the daemon does not yet ship the trace surface
// (the trace is in-process state owned by the resolver); we fall
// back to the batch path for --explain so spec authors still see
// the ladder.
func runSelectorsTestDaemon(ctx context.Context, cmd *cobra.Command, ws *daemon.Workspace, name string) error {
	explain, _ := cmd.Flags().GetBool("explain")
	if explain {
		// Trace is in-process; daemon path doesn't carry it yet.
		return runSelectorsTestBatch(ctx, cmd, ws, name)
	}
	handle, err := ResolveRoute(ctx, ws, RouteOptions{})
	if err != nil {
		// Fall back to batch if the route helper itself errored.
		// (Daemon unavailability is already handled inside
		// ResolveRoute, which degrades to ModeBatch for reads.)
		return runSelectorsTestBatch(ctx, cmd, ws, name)
	}
	defer func() { _ = handle.Close() }()
	if handle.Mode == ModeBatch {
		// Read-only carve-out: no daemon running → batch path.
		return runSelectorsTestBatch(ctx, cmd, ws, name)
	}
	var env semantic_overlay.ResolutionEnvelope
	if err := handle.Client.Call(ctx, "selectors.test", map[string]string{"name": name}, &env); err != nil {
		return err
	}
	return renderSelectorsTest(cmd, &env, nil)
}

func renderSelectorsTest(cmd *cobra.Command, env *semantic_overlay.ResolutionEnvelope, trace []semantic_overlay.AnchorTrace) error {
	if env == nil {
		return fmt.Errorf("nil envelope")
	}
	explain, _ := cmd.Flags().GetBool("explain")
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		payload := map[string]any{"envelope": env}
		if explain {
			payload["trace"] = semantic_overlay.SortedTrace(trace)
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(payload)
	}
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "selector %s\n", env.SelectorID)
	_, _ = fmt.Fprintf(out, "  outcome:    %s\n", env.Outcome)
	_, _ = fmt.Fprintf(out, "  matches:    %d\n", len(env.Matches))
	for _, m := range env.Matches {
		_, _ = fmt.Fprintf(out, "    - id:               %s\n", m.EntityID)
		_, _ = fmt.Fprintf(out, "      qualified_name:   %s\n", m.QualifiedName)
		_, _ = fmt.Fprintf(out, "      confidence:       %.2f\n", m.Confidence)
		_, _ = fmt.Fprintf(out, "      via_anchor:       %s\n", m.ViaAnchor)
	}
	_, _ = fmt.Fprintf(out, "  resolved_at: kernel_event_seq=%d\n", env.ResolvedAt)
	if explain {
		printAnchorLadder(out, semantic_overlay.SortedTrace(trace))
	}
	return nil
}

// printAnchorLadder writes a human-readable rendering of the per-anchor
// evaluation trace. Format mirrors the JSON shape so authors can pivot
// between `--explain` and `--explain --json` without losing context.
func printAnchorLadder(out io.Writer, trace []semantic_overlay.AnchorTrace) {
	_, _ = fmt.Fprintln(out, "  anchor ladder:")
	for _, t := range trace {
		marker := t.Marker
		if marker == "" {
			marker = "anchor"
		}
		head := fmt.Sprintf("    [%d] %s %-22s", t.Index, marker, t.Kind)
		switch {
		case t.Skipped && t.Outcome == "":
			_, _ = fmt.Fprintf(out, "%s — skipped (%s)\n", head, t.Reason)
		case t.Outcome != "":
			_, _ = fmt.Fprintf(out, "%s — score=%.2f → %s (%s)\n",
				head, t.BestScore, t.Outcome, t.Reason)
		default:
			_, _ = fmt.Fprintf(out, "%s — score=%.2f (threshold=%.2f) %s\n",
				head, t.BestScore, t.Threshold, t.Reason)
		}
		for _, m := range t.Matches {
			_, _ = fmt.Fprintf(out, "         - %s (id=%s, conf=%.2f) %s\n",
				m.QualifiedName, shortID(m.EntityID), m.Confidence, m.Detail)
		}
	}
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// newFlowsListCmd implements `graph-harness flows list` (P0.T27 +
// P0.5.T15 daemon routing). In batch mode (`--batch` or
// `GRAPH_HARNESS_BATCH=1`) the overlay is loaded directly from disk
// and the listing is computed in-process; otherwise the command
// auto-spawns the daemon and proxies through the `flows.list` JSON-RPC
// method (which lowers through `Kernel.Route`).
func newFlowsListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "List flows declared in the semantic overlay",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			batch, _ := cmd.Flags().GetBool("batch")
			asJSON, _ := cmd.Flags().GetBool("json")
			ctx := context.Background()

			type flowRow struct {
				Name        string `json:"name"`
				Description string `json:"description,omitempty"`
				Scope       string `json:"scope,omitempty"`
				Steps       int    `json:"steps"`
			}
			var rows []flowRow

			// Daemon path → flows.list (lowered through router).
			if !batch {
				handle, herr := ResolveRoute(ctx, ws, RouteOptions{})
				if herr == nil {
					defer func() { _ = handle.Close() }()
					if handle.Mode == ModeDaemon {
						var resp struct {
							Flows []flowRow `json:"flows"`
						}
						if err := handle.Client.Call(ctx, "flows.list", nil, &resp); err == nil {
							rows = resp.Flows
						}
					}
				}
			}
			if rows == nil {
				// Batch path or daemon fallback: load overlay
				// from disk directly.
				overlay, err := loadOverlay(ws)
				if err != nil {
					return err
				}
				names := make([]string, 0, len(overlay.Flows))
				for n := range overlay.Flows {
					names = append(names, n)
				}
				sort.Strings(names)
				rows = make([]flowRow, 0, len(names))
				for _, n := range names {
					f := overlay.Flows[n]
					rows = append(rows, flowRow{
						Name:        f.Name,
						Description: f.Description,
						Scope:       f.Scope,
						Steps:       len(f.Steps),
					})
				}
			}

			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
			}
			for _, f := range rows {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%-30s %d steps\n", f.Name, f.Steps)
			}
			return nil
		},
	}
	c.Flags().Bool("json", false, "emit JSON")
	return c
}

// newValidateDiffCmdReal builds the real RunE for `graph-harness validate-diff`.
// Wrapped in stubs.go to compose flag definitions.
//
// Daemon-canonical when running interactively (auto-spawns + proxies
// through validate.diff JSON-RPC); batch-mode when `--batch` or
// `GRAPH_HARNESS_BATCH=1` is set — opens layer SQLite read-only, pins
// to head seq, runs the pipeline in-process. P0.5.T15 + P0.5.T16.
func newValidateDiffCmdReal() *cobra.Command {
	return &cobra.Command{
		Use:   "validate-diff",
		Short: "Run the change.process pipeline against a unified diff",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			ctx := context.Background()
			diffPath, _ := cmd.Flags().GetString("diff")
			var unified []byte
			switch diffPath {
			case "", "-":
				unified, err = io.ReadAll(cmd.InOrStdin())
			default:
				// #nosec G304 -- operator-supplied path; standard CLI behavior.
				unified, err = os.ReadFile(diffPath)
			}
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(unified)) == "" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "0 findings (empty diff)")
				return nil
			}
			batch, _ := cmd.Flags().GetBool("batch")
			asJSON, _ := cmd.Flags().GetBool("json")

			var res *change_process.ValidateDiffResult
			// Daemon path: JSON-RPC validate.diff (only if a daemon
			// is already running; ResolveRoute degrades to batch
			// otherwise per SPEC §9.11).
			if !batch {
				handle, herr := ResolveRoute(ctx, ws, RouteOptions{})
				if herr == nil {
					defer func() { _ = handle.Close() }()
					if handle.Mode == ModeDaemon {
						var out change_process.ValidateDiffResult
						if err := handle.Client.Call(ctx, "validate.diff", map[string]string{"diff": string(unified)}, &out); err == nil {
							res = &out
						}
					}
					// fall through to batch on RPC failure / ModeBatch
				}
			}
			if res == nil {
				out, err := runValidateDiffInProcess(ctx, ws, cmd, unified)
				if err != nil {
					return err
				}
				res = out
			}

			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
			}
			outW := cmd.OutOrStdout()
			_, _ = fmt.Fprintln(outW, "─── Validation Result ──────────────────────────")
			_, _ = fmt.Fprintf(outW, "%s\n\n", res.Summary)
			for _, f := range res.Findings {
				_, _ = fmt.Fprintf(outW, "▼ %s — %s\n", f.ID, f.Kind)
				_, _ = fmt.Fprintf(outW, "  Subject: %s (%s)\n", f.Subject.Qualified, f.Subject.EntityKind)
				_, _ = fmt.Fprintf(outW, "  Flow:    %s\n", f.Subject.Flow)
				_, _ = fmt.Fprintln(outW, "  Evidence:")
				for _, e := range f.Evidence {
					_, _ = fmt.Fprintf(outW, "    - %s: %s\n", e.Kind, e.Detail)
				}
				_, _ = fmt.Fprintln(outW, "  Repair:  (deferred to P3)")
			}
			_, _ = fmt.Fprintln(outW, "─────────────────────────────────────────────────")
			return nil
		},
	}
}

// runValidateDiffInProcess runs the validate-diff pipeline directly
// against the workspace's layer stores. Used by `--batch` and as the
// daemon-unavailable fallback. The pipeline pins to the kernel head
// at entry so the same diff at the same seq produces deterministic
// output — the property P0.5.T17 gates.
func runValidateDiffInProcess(
	ctx context.Context,
	ws *daemon.Workspace,
	cmd *cobra.Command,
	unified []byte,
) (*change_process.ValidateDiffResult, error) {
	log, db, store, closeAll, err := openIndexedStore(ctx, ws, cmd)
	if err != nil {
		return nil, err
	}
	defer closeAll()
	_ = db
	overlay, err := loadOverlay(ws)
	if err != nil {
		return nil, err
	}
	resolver, err := semantic_overlay.NewResolver(overlay, store, nil)
	if err != nil {
		return nil, err
	}
	pipeline := &change_process.Pipeline{
		Overlay:  overlay,
		Code:     store,
		Resolver: resolver,
		Events:   log,
	}
	return pipeline.ValidateDiff(ctx, unified, log.LastSeq())
}
