package cli

import (
	"context"
	"sync"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
	"github.com/shivamstaq/graph-harness/internal/mcp"
)

// newMCPCmdReal implements `graph-harness mcp` (stdio transport).
//
// `tools/list` and `initialize` succeed even without an initialized
// workspace — they describe the static tool/resource surface. `tools/call`
// and `resources/read` lazily open the workspace's kernel handles on the
// first invocation; if the cwd has no `.graph-harness/`, the call returns a
// structured tool error rather than failing the whole adapter.
//
// This shape lets agents query the surface at any time (which is useful for
// tool-discovery flows) and keeps the per-spec gotit assertions for
// `tools/list` working regardless of workspace state.
func newMCPCmdReal() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP adapter (stdio transport)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			var (
				once sync.Once
				res  *daemon.Resources
				svc  *jsonrpc.Service
				err  error
			)
			openSvc := func() (*jsonrpc.Service, error) {
				once.Do(func() {
					ws, e := activeWorkspace()
					if e != nil {
						err = e
						return
					}
					r, e := daemon.Open(ctx, ws)
					if e != nil {
						err = e
						return
					}
					res = r
					svc = jsonrpc.NewService(ws, r.Log, r.Code, r.Queue, r.Registry, r.Overlay)
				})
				return svc, err
			}
			defer func() {
				if res != nil {
					_ = res.Close()
				}
			}()

			adapter := mcp.NewLazyAdapter(openSvc)
			return adapter.Serve(ctx, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}

// Suppress unused import warning when above changes; kept for future flag use.
var _ = context.Background
