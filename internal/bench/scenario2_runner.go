package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/shivamstaq/graph-harness/internal/change_process"
	"github.com/shivamstaq/graph-harness/internal/code_core"
)

// Scenario2Runner is the in-package harness that applies a scenario-2
// seed to a code.core store, runs validate-diff against each target
// diff, and scores the result against the oracle. Centralizing the
// run loop here keeps the CLI command thin and lets tests in
// scenario2_test.go exercise the path end-to-end without spinning
// up cobra.
type Scenario2Runner struct {
	// Pipeline is the change.process pipeline the runner invokes for
	// each diff. Caller owns lifecycle (Overlay + Code wiring).
	Pipeline *change_process.Pipeline
	// Store is the code.core store the seed is applied against. Must
	// match the store backing Pipeline.Code so the seeded entities
	// are visible to the pipeline.
	Store *code_core.Store
	// ValidationSeq is the kernel seq passed to ValidateDiff. The
	// seed PutEntity calls use seq = ValidationSeq so the seeded
	// rows are visible at validation time. Defaults to 1 when zero.
	ValidationSeq uint64
}

// ApplySeed PutEntity-writes every entity in the seed and creates the
// reverse-index bindings declared under SelectorBindings. Mirrors
// pipeline_test.go::seedProducerWithTouch + seedDependent. Idempotent
// at fixed seq (PutEntity is an upsert on id).
//
// SelectorBinding wiring is QN-driven: the runner looks up an existing
// entity in the store whose QN matches the binding's FunctionTarget
// (by suffix, the same algorithm the pipeline uses at Stage 2). When
// the workspace was previously indexed by tree-sitter, the existing
// entity carries tree-sitter's canonical ID — binding to that ID is
// what makes the pipeline's reverse-index walk reach the producer.
//
// Falls back to PutEntity-creating the synthesized function entity
// declared in the seed when no existing entity matches (the
// in-memory test path: nothing indexed yet, the seed authors the
// function so the pipeline's Stage 2 suffix lookup still resolves it).
func (r *Scenario2Runner) ApplySeed(ctx context.Context, seed Scenario2Seed) error {
	seq := r.ValidationSeq
	if seq == 0 {
		seq = 1
	}
	if len(seed.Entities) == 0 && len(seed.SelectorBindings) == 0 {
		return ErrScenario2NoSeed
	}
	for _, e := range seed.Entities {
		if err := r.Store.PutEntity(ctx, code_core.Entity{
			ID:            e.ID,
			Kind:          code_core.EntityKind(e.Kind),
			LanguageID:    e.LanguageID,
			QualifiedName: e.QualifiedName,
		}, seq); err != nil {
			return fmt.Errorf("seed PutEntity %s: %w", e.ID, err)
		}
	}
	for _, b := range seed.SelectorBindings {
		ft := b.FunctionTarget
		// Resolve to whichever entity tree-sitter (or a prior pass)
		// already wrote for this QN — that ID is what the pipeline's
		// Stage 2 suffix lookup will return, so the reverse-index walk
		// MUST start from the same ID. The QN is the join key per
		// plan §P2.T35 ("entity's qualified_name is the shared linking
		// key").
		actualFuncID := ft.ID
		suffix := ft.QualifiedName
		if i := lastDot(suffix); i >= 0 {
			suffix = suffix[i+1:]
		}
		if suffix != "" {
			existing, err := r.Store.LookupByQualifiedNameSuffix(ctx, suffix)
			if err != nil {
				return fmt.Errorf("seed lookup function target %s: %w", ft.QualifiedName, err)
			}
			if existing != nil {
				actualFuncID = existing.ID
			}
		}
		if actualFuncID == ft.ID {
			// No tree-sitter-created entity exists — author the
			// synthesized one so Stage 2's lookup has a row to find.
			if err := r.Store.PutEntity(ctx, code_core.Entity{
				ID:            ft.ID,
				Kind:          code_core.EntityKind(ft.Kind),
				LanguageID:    ft.LanguageID,
				QualifiedName: ft.QualifiedName,
			}, seq); err != nil {
				return fmt.Errorf("seed PutEntity function target %s: %w", ft.ID, err)
			}
		}
		via := b.ViaAnchor
		if via == "" {
			via = "qualified_name"
		}
		if err := r.Store.BindSelector(ctx, actualFuncID, b.SelectorID, "", via, seq); err != nil {
			return fmt.Errorf("seed BindSelector function %s: %w", actualFuncID, err)
		}
		if err := r.Store.BindSelector(ctx, b.ProducerTargetID, b.SelectorID, "", "framework_anchor", seq); err != nil {
			return fmt.Errorf("seed BindSelector producer %s: %w", b.ProducerTargetID, err)
		}
	}
	return nil
}

// lastDot returns the index of the last '.' in s, or -1 if none.
// Small helper kept inline because the only caller is ApplySeed's
// qualified-name suffix derivation; pulling in strings.LastIndexByte
// would add an import just for one call site.
func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

// Run executes every diff in the fixture and folds the per-diff
// scores into a single Scenario2Result. The caller must have called
// ApplySeed first (the runner does not auto-apply so the CLI can
// stage the seed after workspace indexing finishes).
func (r *Scenario2Runner) Run(ctx context.Context, fx Scenario2Fixture) (*Scenario2Result, error) {
	scen := Scenario{
		ID:          fx.Oracle.ScenarioID,
		Name:        fx.Oracle.Name,
		Regime:      fx.Oracle.Regime,
		Description: "scenario 2 polyglot Kafka event-payload propagation",
		Languages:   fx.Oracle.Scenario2Languages(),
	}
	result := &Scenario2Result{
		Scenario:      scen,
		Regime:        fx.Oracle.Regime,
		DetectionAxis: map[string]Scenario2LanguageScore{},
		PassesGate:    true,
	}

	// Seed per-language cells so every language declared in the
	// oracle appears in the report even when matched=0.
	for _, lang := range fx.Oracle.Scenario2Languages() {
		result.DetectionAxis[lang] = Scenario2LanguageScore{}
	}

	for _, d := range fx.Oracle.Diffs {
		diffBytes, ok := fx.DiffBytes[d.DiffID]
		if !ok {
			return nil, fmt.Errorf("diff %q bytes missing — LoadScenario2 should have populated", d.DiffID)
		}
		seq := r.ValidationSeq
		if seq == 0 {
			seq = 1
		}
		pipeRes, err := r.Pipeline.ValidateDiff(ctx, diffBytes, seq)
		if err != nil {
			return nil, fmt.Errorf("validate-diff %s: %w", d.DiffID, err)
		}
		diffResult := scoreScenario2Diff(d, pipeRes)
		result.PerDiff = append(result.PerDiff, diffResult)
		// Fold into scenario-level axis.
		for lang, cell := range diffResult.DetectionAxis {
			agg := result.DetectionAxis[lang]
			agg.Expected += cell.Expected
			agg.Matched += cell.Matched
			result.DetectionAxis[lang] = agg
		}
	}

	// Finalize per-language scores + scenario-level gate.
	for lang, cell := range result.DetectionAxis {
		if cell.Expected > 0 {
			cell.Score = float64(cell.Matched) / float64(cell.Expected)
		} else {
			cell.Score = 1.0
		}
		result.DetectionAxis[lang] = cell
		if cell.Score < Scenario2Gate {
			result.PassesGate = false
		}
	}
	return result, nil
}

// scoreScenario2Diff scores a single diff. Builds a per-language
// detection-axis cell from the expected dependents and counts a match
// when the dependent's entity_id surfaces in any
// `missing_dependent_update` finding's FrameworkContext.Dependents.
//
// Matching is by entity_id (the bench seed authored the ID; the
// pipeline preserves it through Stage 5). The dependent's language is
// the oracle's declared language (so a Jest contract test counts as
// "typescript" even though its kind is ContractTest).
func scoreScenario2Diff(spec Scenario2DiffSpec, pipeRes *change_process.ValidateDiffResult) Scenario2DiffResult {
	out := Scenario2DiffResult{
		DiffID:           spec.DiffID,
		DetectionAxis:    map[string]Scenario2LanguageScore{},
		FindingsObserved: len(pipeRes.Findings),
	}

	// Collect every dependent entity_id observed across every
	// missing_dependent_update finding (a single producer + one
	// finding is the expected shape, but the run loop tolerates
	// multiple — Stage 6 produces one per touched producer).
	observedDeps := map[string]struct{}{}
	for _, f := range pipeRes.Findings {
		if f.Kind != change_process.FindingKindMissingDependentUpdate {
			continue
		}
		if f.FrameworkContext == nil {
			continue
		}
		for _, d := range f.FrameworkContext.Dependents {
			observedDeps[d.ID] = struct{}{}
		}
	}

	// Seed per-language cells.
	missed := []string{}
	for _, exp := range spec.Expected {
		out.FindingsExpected++
		for _, dep := range exp.Dependents {
			cell := out.DetectionAxis[dep.Language]
			cell.Expected++
			if _, hit := observedDeps[dep.EntityID]; hit {
				cell.Matched++
			} else {
				missed = append(missed, dep.Language+":"+dep.EntityID+" ("+dep.Kind+")")
			}
			out.DetectionAxis[dep.Language] = cell
		}
	}

	// Compute per-language scores + per-diff pass-gate.
	passes := true
	for lang, cell := range out.DetectionAxis {
		if cell.Expected > 0 {
			cell.Score = float64(cell.Matched) / float64(cell.Expected)
		} else {
			cell.Score = 1.0
		}
		out.DetectionAxis[lang] = cell
		if cell.Score < Scenario2Gate {
			passes = false
		}
	}
	out.PassesGate = passes
	sort.Strings(missed)
	out.MissedDependents = missed
	return out
}

// AsJSON renders a Scenario2Result as pretty JSON for CLI output.
func (r *Scenario2Result) AsJSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}
