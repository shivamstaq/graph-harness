package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/bench"
	"github.com/shivamstaq/graph-harness/internal/change_process"
	"github.com/shivamstaq/graph-harness/internal/daemon"
)

// newBenchCmdReal implements `graph-harness bench`. P1.J — runs the
// scenario corpus across every requested language variant and emits a
// per-language detection-axis score plus a folded aggregate. Plan §3
// gate criterion 10 is what this command answers.
//
// Scenario layout under tests/testdata/bench/scenario<N>/:
//
//	go/, ts/, py/      — one directory per language variant
//	  oracle.json       — scenario_id, language, expected[]
//	  target.diff       — unified diff fed to change.process
//	  .graph-harness/   — overlay/<file>.gh + workspace skeleton
//	  <source files>    — code that change.process resolves against
//
// The runner opens each variant as an independent workspace, indexes its
// source files, runs the same change.process pipeline against the
// variant's target.diff, and scores against its oracle.json. Cross-
// language aggregation is computed from per-language axis scores.
//
// Default behavior (no --language flag, or --language all) processes
// every variant the scenario directory exposes; explicit values
// (`go`, `typescript`, `python`) restrict to a single variant and emit
// the bare per-scenario shape (back-compatible with the pre-P1 output).
func newBenchCmdReal() *cobra.Command {
	c := &cobra.Command{
		Use:   "bench",
		Short: "Run the bench corpus against the oracle (per-language scoring)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			scenarioID, _ := cmd.Flags().GetInt("scenario")
			regime, _ := cmd.Flags().GetString("regime")
			languageFlag, _ := cmd.Flags().GetString("language")
			rootFlag, _ := cmd.Flags().GetString("scenario-root")
			if scenarioID == 0 {
				scenarioID = 1
			}
			if regime == "" {
				regime = "mature"
			}

			scenarioRoot, err := resolveScenarioRoot(rootFlag, scenarioID)
			if err != nil {
				return err
			}

			languages := []string{}
			if languageFlag != "" && languageFlag != "all" {
				languages = []string{languageFlag}
			}
			scenario, variants, err := bench.LoadScenario(scenarioRoot, scenarioID, languages)
			if err != nil {
				return err
			}
			scenario.Regime = regime

			ctx := context.Background()
			perLang := make(map[string]*bench.Result, len(variants))
			for _, v := range variants {
				result, err := runVariant(ctx, scenario, v)
				if err != nil {
					return fmt.Errorf("%s: %w", v.Language, err)
				}
				perLang[v.Oracle.Language] = result
			}

			out := cmd.OutOrStdout()
			if len(perLang) == 1 && languageFlag != "" && languageFlag != "all" {
				for _, r := range perLang {
					b, _ := r.AsJSON()
					_, _ = fmt.Fprintln(out, string(b))
				}
				return nil
			}

			multi := bench.Aggregate(scenario, perLang)
			b, _ := multi.AsJSON()
			_, _ = fmt.Fprintln(out, string(b))
			return nil
		},
	}
	c.Flags().Int("scenario", 0, "scenario ID (default: 1)")
	c.Flags().String("regime", "mature", "regime to run (mature only in P0/P1)")
	c.Flags().String("language", "all", "language variant (go|typescript|python|all)")
	c.Flags().String("scenario-root", "", "path to tests/testdata/bench/scenario<N>/ (default: discover from cwd / repo / E2E_PROJECT_ROOT env)")
	c.Flags().String("gate", "", "evaluate against a named ship-bar (P6+)")
	return c
}

// resolveScenarioRoot picks the directory the scenario corpus lives in.
// Resolution order:
//
//  1. --scenario-root flag (operator override).
//  2. cwd-walk for tests/testdata/bench/scenario<N>/ above cwd.
//  3. Project root via projectRoot() walk.
//  4. GRAPH_HARNESS_E2E_PROJECT_ROOT env var (gotit-style; the e2e
//     runner exports this so specs in tmpdirs can still reach the
//     repo's bench corpus without copying fixtures into the helper).
func resolveScenarioRoot(flag string, scenarioID int) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if cwd, err := os.Getwd(); err == nil {
		if root, err := bench.DiscoverScenarioRoot(cwd, scenarioID); err == nil {
			return root, nil
		}
	}
	if root2, err := projectRoot(); err == nil {
		if root, err := bench.DiscoverScenarioRoot(root2, scenarioID); err == nil {
			return root, nil
		}
	}
	if envRoot := os.Getenv("GRAPH_HARNESS_E2E_PROJECT_ROOT"); envRoot != "" {
		if root, err := bench.DiscoverScenarioRoot(envRoot, scenarioID); err == nil {
			return root, nil
		}
	}
	return "", fmt.Errorf("locate scenario %d: pass --scenario-root or run from a tree containing tests/testdata/bench/scenario%d/", scenarioID, scenarioID)
}

// runVariant opens a single language variant as a workspace, runs the
// change.process pipeline against its target.diff, and scores the
// result. Each variant gets its own kernel handles via daemon.Open so
// per-language code.core indexing stays isolated.
func runVariant(ctx context.Context, scenario bench.Scenario, v bench.LanguageVariant) (*bench.Result, error) {
	ws, err := daemon.From(v.Path)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	if !ws.IsInitialized() {
		return nil, fmt.Errorf("variant %s missing .graph-harness/ — re-stage the fixture", v.Path)
	}
	// Synthesize the workspace config file if init was never run; the
	// bench fixtures ship overlay files but not graph-harness.toml so
	// daemon.Open's IsInitialized check would otherwise fail.
	if _, err := os.Stat(ws.ConfigPath); os.IsNotExist(err) {
		if err := os.WriteFile(ws.ConfigPath, []byte("# bench fixture (synthesized)\n"), 0o600); err != nil {
			return nil, fmt.Errorf("synthesize config: %w", err)
		}
	}
	res, err := daemon.Open(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer func() { _ = res.Close() }()

	pipeline := &change_process.Pipeline{Overlay: res.Overlay, Code: res.Code}
	pipeRes, err := pipeline.ValidateDiff(ctx, v.Diff, res.Log.LastSeq())
	if err != nil {
		return nil, fmt.Errorf("validate-diff: %w", err)
	}
	perLangScenario := scenario
	perLangScenario.Languages = []string{v.Oracle.Language}
	if v.Oracle.Name != "" {
		perLangScenario.Name = v.Oracle.Name
	}
	return bench.Score(perLangScenario, pipeRes, v.Oracle.Expected), nil
}
