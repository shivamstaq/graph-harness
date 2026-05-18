package cli

import (
	"errors"
	"fmt"
	"sync"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
	"github.com/shivamstaq/graph-harness/internal/mcp"
)

// newMCPCmdReal implements `graph-harness mcp` (stdio transport).
//
// Per F2 / plan/answers/04 §5, MCP routes through the daemon when one
// is running: `tools/call` and `resources/read` lazily resolve a
// jsonrpc.Consumer via ResolveRoute. ModeDaemon hands the adapter a
// *jsonrpc.ClientService; ModeBatch falls back to opening Resources
// locally and wrapping them in *jsonrpc.Service. `tools/list` and
// `initialize` succeed without any consumer bound — they describe
// the static tool surface — so agents can introspect any cwd.
//
// `--check` is a non-interactive smoke surface: opens the consumer
// (auto-spawning the daemon if necessary), prints `mcp: ok` + the
// daemon pid (or local fallback note), then exits 0.
func newMCPCmdReal() *cobra.Command {
	c := &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP adapter (stdio transport)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			check, _ := cmd.Flags().GetBool("check")

			var (
				once   sync.Once
				handle *RouteHandle
				svc    jsonrpc.Consumer
				err    error
			)
			openSvc := func() (jsonrpc.Consumer, error) {
				once.Do(func() {
					ws, e := activeWorkspace()
					if e != nil {
						err = e
						return
					}
					h, e := ResolveRoute(ctx, ws, RouteOptions{})
					if e != nil {
						err = e
						return
					}
					handle = h
					if h.Mode == ModeDaemon {
						svc = jsonrpc.NewClientService(h.Client)
						return
					}
					// Batch fallback — open local Resources and wrap.
					r, e := daemon.Open(ctx, ws)
					if e != nil {
						err = e
						return
					}
					svc = jsonrpc.NewService(ws, r.Log, r.Code, r.Queue, r.Registry, r.Overlay)
					// Register cleanup via a context-bound watcher.
					go func() {
						<-ctx.Done()
						_ = r.Close()
					}()
				})
				if err != nil {
					return nil, err
				}
				if svc == nil {
					return nil, errors.New("mcp: openSvc returned nil consumer")
				}
				return svc, nil
			}
			defer func() {
				if handle != nil {
					_ = handle.Close()
				}
			}()

			if check {
				if _, e := openSvc(); e != nil {
					return fmt.Errorf("mcp --check: %w", e)
				}
				mode := "batch"
				if handle != nil && handle.Mode == ModeDaemon {
					mode = "daemon"
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "mcp: ok (consumer=%s)\n", mode)
				return nil
			}

			adapter := mcp.NewLazyAdapter(openSvc)
			return adapter.Serve(ctx, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	c.Flags().Bool("check", false, "open the consumer and exit 0 (smoke surface for daemon-routed CI)")
	return c
}
