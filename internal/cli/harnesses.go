package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/dsl"
)

// newHarnessesCmd implements `graph-harness harnesses {list,show,explain,check,edit,delete}`
// per F12 / P0.T06 + SPEC §11.1 / §11.2. Harnesses are slotted-form
// `.gh` files under `<workspace>/.graph-harness/harnesses/` that
// pre-author repair recipes, briefing prose, and skill-attached
// region scopes.
//
// Surface:
//
//   - list: enumerates harness files with their first-line summary.
//   - show: prints the raw `.gh` source.
//   - explain: parses the harness via dsl.ParseString and prints the
//     declared decls (selectors, flows, queries) — the local-only
//     half of the kernel substrate explanation. A future P3 task
//     will deepen this to attach the full kernel state at the
//     current seq.
//   - check: parse-only validation. The full validate-diff
//     scoped-to-harness path is deferred to P3 (Mangle rule pack).
//   - edit: opens the harness file in $EDITOR; re-parses on save.
//   - delete: removes the harness file (with --yes confirmation).
//
// For local-only operations (list/show/edit/delete/parse-explain)
// we walk the filesystem directly — these are read paths over the
// workspace's own .gh files and don't require daemon routing. A
// future iteration can lift them through Kernel.Route so the daemon
// becomes the source-of-truth for harness state.
func newHarnessesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "harnesses",
		Short: "Manage workspace harness bundles (P0.T06 / SPEC §11.1)",
	}
	c.AddCommand(
		newHarnessesListCmd(),
		newHarnessesShowCmd(),
		newHarnessesExplainCmd(),
		newHarnessesCheckCmd(),
		newHarnessesEditCmd(),
		newHarnessesDeleteCmd(),
	)
	return c
}

func harnessesDir(rootStateDir string) string {
	return filepath.Join(rootStateDir, "harnesses")
}

func listHarnessFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".gh" {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

func newHarnessesListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "List harness files with first-line summary",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			dir := harnessesDir(ws.StateDir)
			files, err := listHarnessFiles(dir)
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			type row struct {
				Name    string `json:"name"`
				Path    string `json:"path"`
				Summary string `json:"summary,omitempty"`
			}
			rows := make([]row, 0, len(files))
			for _, name := range files {
				path := filepath.Join(dir, name)
				summary := firstNonBlankLine(path)
				rows = append(rows, row{Name: strings.TrimSuffix(name, ".gh"), Path: path, Summary: summary})
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
			}
			if len(rows) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "(no harnesses; create one as .graph-harness/harnesses/<name>.gh)")
				return nil
			}
			for _, r := range rows {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%-30s %s\n", r.Name, r.Summary)
			}
			return nil
		},
	}
	c.Flags().Bool("json", false, "emit JSON")
	return c
}

func newHarnessesShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Print a harness's raw .gh source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			path := filepath.Join(harnessesDir(ws.StateDir), args[0]+".gh")
			data, err := os.ReadFile(path) //nolint:gosec // workspace-scoped harness path
			if err != nil {
				return err
			}
			_, _ = cmd.OutOrStdout().Write(data)
			return nil
		},
	}
}

func newHarnessesExplainCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "explain <name>",
		Short: "Parse a harness and list its declarations (local-only; kernel substrate attach is P3)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			path := filepath.Join(harnessesDir(ws.StateDir), args[0]+".gh")
			data, err := os.ReadFile(path) //nolint:gosec
			if err != nil {
				return err
			}
			f, err := dsl.ParseString(args[0]+".gh", string(data))
			if err != nil {
				return fmt.Errorf("parse %s: %w", path, err)
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "harness %s (%d declarations)\n", args[0], len(f.Decls))
			for i, d := range f.Decls {
				switch {
				case d.Selector != nil:
					_, _ = fmt.Fprintf(out, "  [%d] selector %s\n", i+1, d.Selector.Name)
				case d.Flow != nil:
					_, _ = fmt.Fprintf(out, "  [%d] flow %s\n", i+1, d.Flow.Name)
				case d.Query != nil:
					_, _ = fmt.Fprintf(out, "  [%d] query %s\n", i+1, d.Query.Name)
				case d.Import != nil:
					_, _ = fmt.Fprintf(out, "  [%d] import %s as %s\n", i+1, d.Import.Path, d.Import.Alias)
				}
			}
			return nil
		},
	}
}

func newHarnessesCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check <name>",
		Short: "Parse-validate a harness (full validate-diff scoping is P3)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			path := filepath.Join(harnessesDir(ws.StateDir), args[0]+".gh")
			data, err := os.ReadFile(path) //nolint:gosec
			if err != nil {
				return err
			}
			if _, err := dsl.ParseString(args[0]+".gh", string(data)); err != nil {
				return fmt.Errorf("parse %s: %w", path, err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "ok %s\n", path)
			return nil
		},
	}
}

func newHarnessesEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit <name>",
		Short: "Edit a harness in $EDITOR (re-parses on save; reverts on parse error)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			dir := harnessesDir(ws.StateDir)
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return err
			}
			path := filepath.Join(dir, args[0]+".gh")
			prev, readErr := os.ReadFile(path) //nolint:gosec
			if readErr != nil && !os.IsNotExist(readErr) {
				return readErr
			}
			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "vi"
			}
			edit := exec.Command(editor, path) //nolint:gosec // operator-supplied editor
			edit.Stdin = os.Stdin
			edit.Stdout = cmd.OutOrStdout()
			edit.Stderr = cmd.ErrOrStderr()
			if err := edit.Run(); err != nil {
				return fmt.Errorf("editor exited: %w", err)
			}
			cur, err := os.ReadFile(path) //nolint:gosec
			if err != nil {
				return err
			}
			if _, parseErr := dsl.ParseString(args[0]+".gh", string(cur)); parseErr != nil {
				if prev != nil {
					_ = os.WriteFile(path, prev, 0o600)
				}
				return fmt.Errorf("post-edit parse failed; reverted: %w", parseErr)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "validated %s\n", path)
			return nil
		},
	}
}

func newHarnessesDeleteCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "delete <name>",
		Short: "Remove a harness file (use --yes to skip confirmation)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			yes, _ := cmd.Flags().GetBool("yes")
			path := filepath.Join(harnessesDir(ws.StateDir), args[0]+".gh")
			if !yes {
				return fmt.Errorf("refusing to delete without --yes (path %s)", path)
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", path)
			return nil
		},
	}
	c.Flags().Bool("yes", false, "confirm deletion")
	return c
}

func firstNonBlankLine(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
