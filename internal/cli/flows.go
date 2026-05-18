package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/dsl"
)

// newFlowsCreateCmd implements `graph-harness flows create` per F12 /
// P0.T27. Synthesizes a minimal flow .gh declaration from CLI flags,
// validates it via dsl.ParseString, and writes to
// `.graph-harness/overlay/flows/<name>.gh`.
func newFlowsCreateCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "create",
		Short: "Scaffold a new flow declaration (writes .graph-harness/overlay/flows/<name>.gh)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			name, _ := cmd.Flags().GetString("name")
			description, _ := cmd.Flags().GetString("description")
			scope, _ := cmd.Flags().GetString("scope")
			steps, _ := cmd.Flags().GetStringArray("step")
			if name == "" {
				return fmt.Errorf("--name required")
			}

			var b strings.Builder
			fmt.Fprintf(&b, "flow %s {\n", name)
			if description != "" {
				fmt.Fprintf(&b, "  description %q\n", description)
			}
			if scope != "" {
				fmt.Fprintf(&b, "  scope %s\n", scope)
			}
			for i, s := range steps {
				stepName := fmt.Sprintf("Step%d", i+1)
				fmt.Fprintf(&b, "  step %s targets %s\n", stepName, s)
			}
			b.WriteString("}\n")
			source := b.String()

			// Parse-validate before writing — a malformed `.gh` should
			// never land on disk.
			if _, err := dsl.ParseString(name+".gh", source); err != nil {
				return fmt.Errorf("invalid flow declaration: %w", err)
			}

			dir := filepath.Join(ws.OverlayDir, "flows")
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return err
			}
			path := filepath.Join(dir, name+".gh")
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", path)
			return nil
		},
	}
	c.Flags().String("name", "", "flow name (required)")
	c.Flags().String("description", "", "human-readable description")
	c.Flags().String("scope", "", "selector that scopes the flow")
	c.Flags().StringArray("step", nil, "step target selector (repeat for multiple steps)")
	return c
}

// newFlowsEditCmd implements `graph-harness flows edit <name>` per
// F12 / P0.T27. Opens `$EDITOR` (fallback `vi`) on the flow's `.gh`
// file. On editor exit, re-parses the result; if parsing fails, the
// edit is rejected and the prior content is restored.
func newFlowsEditCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "edit <name>",
		Short: "Edit a flow in $EDITOR (parses on save; rejects invalid)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			path := filepath.Join(ws.OverlayDir, "flows", args[0]+".gh")
			prev, err := os.ReadFile(path) //nolint:gosec // overlay path is workspace-constrained
			if err != nil {
				return fmt.Errorf("flow %s not found at %s: %w", args[0], path, err)
			}

			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "vi"
			}
			editorCmd := exec.Command(editor, path) //nolint:gosec // operator-supplied editor; standard CLI behavior
			editorCmd.Stdin = os.Stdin
			editorCmd.Stdout = cmd.OutOrStdout()
			editorCmd.Stderr = cmd.ErrOrStderr()
			if err := editorCmd.Run(); err != nil {
				return fmt.Errorf("editor exited: %w", err)
			}

			cur, err := os.ReadFile(path) //nolint:gosec
			if err != nil {
				return err
			}
			if _, parseErr := dsl.ParseString(args[0]+".gh", string(cur)); parseErr != nil {
				// Restore prior content; the user's edit is invalid.
				_ = os.WriteFile(path, prev, 0o600)
				return fmt.Errorf("post-edit parse failed; reverted to prior content: %w", parseErr)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "validated %s\n", path)
			return nil
		},
	}
	return c
}
