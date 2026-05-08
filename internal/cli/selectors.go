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
			env, trace, err := overlay.ResolveWithTrace(ctx, args[0], store, log.LastSeq())
			if err != nil {
				return err
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
		},
	}
	c.Flags().Bool("json", false, "emit envelope as JSON")
	c.Flags().Bool("explain", false, "print the full multi-anchor ladder evaluation")
	addExtractorToggleFlags(c)
	return c
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

// newFlowsListCmd implements `graph-harness flows list` (P0.T27).
func newFlowsListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "List flows declared in the semantic overlay",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			overlay, err := loadOverlay(ws)
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			names := make([]string, 0, len(overlay.Flows))
			for n := range overlay.Flows {
				names = append(names, n)
			}
			sort.Strings(names)
			if asJSON {
				rows := make([]map[string]any, 0, len(names))
				for _, n := range names {
					f := overlay.Flows[n]
					rows = append(rows, map[string]any{
						"name":        f.Name,
						"description": f.Description,
						"scope":       f.Scope,
						"steps":       len(f.Steps),
					})
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
			}
			for _, n := range names {
				f := overlay.Flows[n]
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%-30s %d steps\n", f.Name, len(f.Steps))
			}
			return nil
		},
	}
	c.Flags().Bool("json", false, "emit JSON")
	return c
}

// newValidateDiffCmdReal builds the real RunE for `graph-harness validate-diff`.
// Wrapped in stubs.go to compose flag definitions.
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
			// Wire P1 finding sources: the multi-anchor resolver supplies
			// before/after envelopes for unresolved_anchor detection, and
			// the event log supplies code.core.SymbolDisambiguation events
			// for the symbol_disambiguation finding kind.
			resolver, err := semantic_overlay.NewResolver(overlay, store, nil)
			if err != nil {
				return err
			}
			pipeline := &change_process.Pipeline{
				Overlay:  overlay,
				Code:     store,
				Resolver: resolver,
				Events:   log,
			}
			res, err := pipeline.ValidateDiff(ctx, unified, log.LastSeq())
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintln(out, "─── Validation Result ──────────────────────────")
			_, _ = fmt.Fprintf(out, "%s\n\n", res.Summary)
			for _, f := range res.Findings {
				_, _ = fmt.Fprintf(out, "▼ %s — %s\n", f.ID, f.Kind)
				_, _ = fmt.Fprintf(out, "  Subject: %s (%s)\n", f.Subject.Qualified, f.Subject.EntityKind)
				_, _ = fmt.Fprintf(out, "  Flow:    %s\n", f.Subject.Flow)
				_, _ = fmt.Fprintln(out, "  Evidence:")
				for _, e := range f.Evidence {
					_, _ = fmt.Fprintf(out, "    - %s: %s\n", e.Kind, e.Detail)
				}
				_, _ = fmt.Fprintln(out, "  Repair:  (deferred to P3)")
			}
			_, _ = fmt.Fprintln(out, "─────────────────────────────────────────────────")
			return nil
		},
	}
}
