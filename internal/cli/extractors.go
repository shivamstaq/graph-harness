// Package cli — `graph-harness extractors {list|enable|disable|status}`
// (P2.T05). Routes through ResolveRoute so writers (enable, disable)
// go through the daemon's single-writer path while reads (list,
// status) work in batch mode without spawning the daemon.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
)

func newExtractorsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "extractors",
		Short: "Manage code.framework extractor plugins (P2)",
		Long: `extractors list / enable / disable / status

The code.framework layer's per-framework extractors (HTTP routes,
event publishers/subscribers, DB schemas, tests, generated artifacts)
are compiled into the kernel binary at build time. Use the subcommands
to inspect what's registered, see live counters, and enable/disable
individual extractors at runtime (persists to
.graph-harness/extractors.toml).`,
	}
	c.AddCommand(
		newExtractorsListCmd(),
		newExtractorsStatusCmd(),
		newExtractorsEnableCmd(),
		newExtractorsDisableCmd(),
	)
	return c
}

func newExtractorsListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "List every registered framework extractor",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			handle, err := ResolveRoute(ctx, ws, RouteOptions{})
			if err != nil {
				return err
			}
			defer func() { _ = handle.Close() }()

			descs, err := callExtractorsList(ctx, handle)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return json.NewEncoder(out).Encode(descs)
			}
			if len(descs) == 0 {
				_, _ = fmt.Fprintln(out, "(no framework extractors registered)")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "NAME\tFAMILY\tLANGUAGES\tFRAMEWORKS")
			for _, d := range descs {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
					d.Name, d.Family,
					strings.Join(d.Languages, ","), strings.Join(d.Frameworks, ","))
			}
			return tw.Flush()
		},
	}
	c.Flags().Bool("json", false, "emit JSON")
	return c
}

func newExtractorsStatusCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "status [name...]",
		Short: "Show live extractor status (enabled, counters, last error)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			handle, err := ResolveRoute(ctx, ws, RouteOptions{})
			if err != nil {
				return err
			}
			defer func() { _ = handle.Close() }()
			statuses, err := callExtractorsStatus(ctx, handle, args)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return json.NewEncoder(out).Encode(statuses)
			}
			if len(statuses) == 0 {
				_, _ = fmt.Fprintln(out, "(no matching extractors)")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "NAME\tENABLED\tEVENTS_IN\tEVENTS_OUT\tERRORS\tLAST_ERROR")
			for _, s := range statuses {
				le := s.LastError
				if len(le) > 40 {
					le = le[:37] + "..."
				}
				_, _ = fmt.Fprintf(tw, "%s\t%v\t%d\t%d\t%d\t%s\n",
					s.Name, s.Enabled, s.EventsIn, s.EventsOut, s.Errors, le)
			}
			return tw.Flush()
		},
	}
	c.Flags().Bool("json", false, "emit JSON")
	return c
}

func newExtractorsEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <name>",
		Short: "Enable an extractor at runtime + persist the choice",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			handle, err := ResolveRoute(ctx, ws, RouteOptions{RequireWriter: true})
			if err != nil {
				return err
			}
			defer func() { _ = handle.Close() }()
			var res jsonrpc.ExtractorsEnableResult
			if err := handle.Client.Call(ctx, "extractors.enable",
				jsonrpc.ExtractorsEnableParams{Name: args[0]}, &res); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "extractor %q enabled\n", args[0])
			return nil
		},
	}
}

func newExtractorsDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <name>",
		Short: "Disable an extractor at runtime + persist the choice",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			handle, err := ResolveRoute(ctx, ws, RouteOptions{RequireWriter: true})
			if err != nil {
				return err
			}
			defer func() { _ = handle.Close() }()
			var res jsonrpc.ExtractorsDisableResult
			if err := handle.Client.Call(ctx, "extractors.disable",
				jsonrpc.ExtractorsDisableParams{Name: args[0]}, &res); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "extractor %q disabled\n", args[0])
			return nil
		},
	}
}

// callExtractorsList works in both ModeDaemon and ModeBatch. The batch
// path returns the package-level registry directly so a one-shot CLI
// can list available extractors without daemon spawn.
func callExtractorsList(ctx context.Context, handle *RouteHandle) ([]code_framework.Descriptor, error) {
	if handle.Mode == ModeDaemon {
		var res jsonrpc.ExtractorsListResult
		if err := handle.Client.Call(ctx, "extractors.list", struct{}{}, &res); err != nil {
			return nil, err
		}
		return res.Extractors, nil
	}
	return code_framework.Descriptors(), nil
}

// callExtractorsStatus returns the live status snapshot. In batch
// mode there is no live dispatcher, so we derive a static snapshot
// from the registry + workspace TOML (Enabled reflects the persisted
// preference; counters are zero).
func callExtractorsStatus(ctx context.Context, handle *RouteHandle, names []string) ([]code_framework.ExtractorStatus, error) {
	if handle.Mode == ModeDaemon {
		var res jsonrpc.ExtractorsStatusResult
		if err := handle.Client.Call(ctx, "extractors.status",
			jsonrpc.ExtractorsStatusParams{Names: names}, &res); err != nil {
			return nil, err
		}
		return res.Extractors, nil
	}
	// Batch path: derive a static snapshot.
	cfg, err := code_framework.LoadConfig(handle.Workspace.Root)
	if err != nil {
		return nil, err
	}
	want := func(n string) bool {
		if len(names) == 0 {
			return true
		}
		for _, w := range names {
			if w == n {
				return true
			}
		}
		return false
	}
	out := make([]code_framework.ExtractorStatus, 0, len(code_framework.Descriptors()))
	for _, d := range code_framework.Descriptors() {
		if !want(d.Name) {
			continue
		}
		out = append(out, code_framework.ExtractorStatus{
			Name:       d.Name,
			Family:     d.Family,
			Languages:  d.Languages,
			Frameworks: d.Frameworks,
			Inputs:     d.Inputs,
			Outputs:    d.Outputs,
			Enabled:    !cfg.IsDisabled(d.Name),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
