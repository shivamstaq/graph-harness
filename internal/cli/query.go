package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/dsl"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
)

// newQueryCmdReal implements `graph-harness query <dsl>` (P0.T17). v0
// supports the four declaration kinds; full Cypher evaluation lands at P3.
// Until then, query parses the DSL, validates Phase-0 surface restrictions,
// and emits a JSON AST envelope so agents can compose against the grammar
// even before evaluation is wired.
//
// Daemon-canonical routing (P1.5.T06): when a daemon is running, the
// query parses through the daemon's query.parse RPC so the workspace
// stays single-writer. Without a daemon the command opens the event
// log read-only — same SPEC §9.11 fallback as `selectors test`.
func newQueryCmdReal() *cobra.Command {
	return &cobra.Command{
		Use:   "query [dsl]",
		Short: "Run a DSL query against the kernel",
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			var src string
			if len(args) > 0 {
				src = strings.Join(args, " ")
			}
			if src == "" {
				return fmt.Errorf("query DSL is required (positional arg or stdin)")
			}

			ctx := cmd.Context()
			handle, err := ResolveRoute(ctx, ws, RouteOptions{})
			if err != nil {
				return err
			}
			defer func() { _ = handle.Close() }()
			out := cmd.OutOrStdout()

			if handle.Mode == ModeDaemon {
				var res jsonrpc.QueryParseResult
				if err := handle.Client.Call(ctx, "query.parse",
					jsonrpc.QueryParseParams{Source: src}, &res); err != nil {
					return err
				}
				return json.NewEncoder(out).Encode(map[string]any{
					"resolved_at_kernel_seq": res.ResolvedAtKernelSeq,
					"declarations":           res.DeclarationCount,
					"canonical_gh":           res.CanonicalGH,
				})
			}

			// Batch fallback (daemon unavailable).
			log, err := facts.OpenEventLog(ws.EventLog)
			if err != nil {
				return err
			}
			defer func() { _ = log.Close() }()
			f, err := dsl.ParseString("inline.gh", src)
			if err != nil {
				return err
			}
			rendered := dsl.Render(f)
			env := map[string]any{
				"resolved_at_kernel_seq": log.LastSeq(),
				"declarations":           len(f.Decls),
				"canonical_gh":           rendered,
			}
			return json.NewEncoder(out).Encode(env)
		},
	}
}

// newReplCmdReal is an interactive DSL REPL. P0.T17.
func newReplCmdReal() *cobra.Command {
	return &cobra.Command{
		Use:   "repl",
		Short: "Interactive DSL REPL",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			scanner := bufio.NewScanner(cmd.InOrStdin())
			_, _ = fmt.Fprintln(out, "graph-harness DSL REPL — Phase 0")
			_, _ = fmt.Fprintln(out, "type a declaration; blank line evaluates; Ctrl-D exits")
			var buf strings.Builder
			for scanner.Scan() {
				line := scanner.Text()
				if line == "" {
					if buf.Len() == 0 {
						continue
					}
					f, err := dsl.ParseString("repl.gh", buf.String())
					if err != nil {
						_, _ = fmt.Fprintf(out, "× %v\n", err)
					} else {
						_, _ = fmt.Fprintf(out, "✓ parsed %d declaration(s)\n", len(f.Decls))
					}
					buf.Reset()
					continue
				}
				buf.WriteString(line + "\n")
			}
			_ = context.Background
			return nil
		},
	}
}
