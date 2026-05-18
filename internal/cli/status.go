package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// newStatusCmd implements `graph-harness status`. P0.T14 +
// P0.5.T15 daemon routing: when the daemon is running, asks it for
// status; otherwise reads the event log directly.
func newStatusCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: "Workspace + daemon status summary",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := daemon.Discover()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !ws.IsInitialized() {
				_, _ = fmt.Fprintln(out, "no .graph-harness workspace found in or above cwd; run `graph-harness init` first")
				return fmt.Errorf("workspace not initialized")
			}
			_, _ = fmt.Fprintf(out, "Workspace:   %s\n", ws.Root)
			_, _ = fmt.Fprintf(out, "Workspace ID: %s\n", ws.ID)
			_, _ = fmt.Fprintf(out, "Socket:      %s\n", ws.SocketPath)
			_, _ = fmt.Fprintf(out, "Event log:   %s\n", ws.EventLog)

			// Daemon-canonical seq read: dial if running so we
			// avoid the SQLite open path. status does not auto-
			// spawn the daemon — it is a probe command and must
			// not have side-effects.
			seq := uint64(0)
			daemonStatus := "not running"
			st := daemon.Inspect(ws)
			if st.Running {
				daemonStatus = fmt.Sprintf("running (pid=%d)", st.PID)
				if client, derr := jsonrpc.Dial(cmd.Context(), ws.SocketPath); derr == nil {
					var rpcStat struct {
						LastSeq uint64 `json:"last_seq"`
					}
					if err := client.Call(cmd.Context(), "status", nil, &rpcStat); err == nil {
						seq = rpcStat.LastSeq
					}
					_ = client.Close()
				}
			}
			if seq == 0 {
				seq, _ = tryLastSeq(ws.EventLog)
			}
			_, _ = fmt.Fprintf(out, "Daemon:      %s\n", daemonStatus)
			_, _ = fmt.Fprintf(out, "Last seq:    %d\n", seq)
			return nil
		},
	}
	return c
}

// newLayersCmd implements `graph-harness layers list`. P0.T14.
func newLayersCmd() *cobra.Command {
	c := &cobra.Command{Use: "layers", Short: "Inspect installed kernel layers"}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List installed layers and their states",
		RunE: func(cmd *cobra.Command, _ []string) error {
			reg, err := loadProjectRegistry()
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			out := cmd.OutOrStdout()
			names := reg.List()
			if asJSON {
				rows := make([]map[string]any, 0, len(names))
				for _, n := range names {
					m, _ := reg.Get(n)
					rows = append(rows, map[string]any{
						"name":       m.Metadata.Name,
						"version":    m.Metadata.Version,
						"depends_on": m.Metadata.DependsOn,
					})
				}
				return json.NewEncoder(out).Encode(rows)
			}
			for _, n := range names {
				m, _ := reg.Get(n)
				_, _ = fmt.Fprintf(out, "%-22s v%s\n", m.Metadata.Name, m.Metadata.Version)
			}
			return nil
		},
	}
	listCmd.Flags().Bool("json", false, "emit JSON instead of table")

	installCmd := &cobra.Command{
		Use:   "install <manifest.yaml>",
		Short: "Validate and register a layer manifest",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			// #nosec G304 -- operator-supplied path; standard CLI behavior.
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			m, err := kernel.ParseManifest(data)
			if err != nil {
				return err
			}
			if err := m.Validate(); err != nil {
				return err
			}
			if dryRun {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "✓ %s v%s validates\n", m.Metadata.Name, m.Metadata.Version)
				return nil
			}
			// Real install (with deps) requires a populated registry; for now
			// we validate only. P0.T14 graduation when daemon RPC lands.
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "✓ %s v%s validated (full install via daemon RPC arrives with P0.T13)\n",
				m.Metadata.Name, m.Metadata.Version)
			return nil
		},
	}
	installCmd.Flags().Bool("dry-run", false, "validate without registering")

	c.AddCommand(listCmd, installCmd)
	return c
}

// loadProjectRegistry returns a registry populated with the manifests baked
// into the binary at build time. Falls back to the project's manifests/
// directory when running from a development tree (useful before `go install`).
func loadProjectRegistry() (*kernel.Registry, error) {
	reg := kernel.NewRegistry()
	if err := reg.LoadEmbeddedManifests(); err == nil {
		return reg, nil
	}
	// Dev fallback: walk up to a manifests/ dir.
	root, err := projectRoot()
	if err != nil {
		return nil, err
	}
	if err := reg.LoadDir(filepath.Join(root, "manifests")); err != nil {
		return nil, err
	}
	return reg, nil
}

func projectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

func tryLastSeq(dbPath string) (uint64, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return 0, err
	}
	log, err := facts.OpenEventLog(dbPath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = log.Close() }()
	_ = context.Background
	return log.LastSeq(), nil
}
