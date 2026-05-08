// Package change_process implements the 12-stage validation pipeline.
//
// Phase 1 emits three finding kinds:
//
//   - `flow_unreviewed` (stage 10) — touched flow-scoped function without
//     acknowledging the flow (Phase 0 carryover).
//   - `unresolved_anchor` (stage 10) — a selector that was bound at
//     validation_seq − 1 is unresolved at validation_seq, indicating
//     a code edit broke an authored binding without a fall-through anchor
//     catching it (SPEC §8.2 + plan §P1.T30).
//   - `symbol_disambiguation` (stage 6) — surfaces
//     `code.core.SymbolDisambiguation` events as a finding kind so the
//     reviewer sees the conflicting source claims inline (plan §P1.T31).
//
// Empty repair payload until P3 ships RepairInstruction synthesis. The
// pipeline is idempotent at a fixed kernel sequence (SPEC §8.1).
package change_process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/semantic_overlay"
)

// ValidationFinding is the SPEC §8.2 shape. The CLI emits this as JSON via
// `validate-diff --json`. P1 finding kinds: `flow_unreviewed`,
// `unresolved_anchor`, `symbol_disambiguation`.
type ValidationFinding struct {
	ID       string         `json:"id"`
	Kind     string         `json:"kind"`     // flow_unreviewed | unresolved_anchor | symbol_disambiguation
	Severity string         `json:"severity"` // info | low | medium | high | critical
	Subject  Subject        `json:"subject"`
	Evidence []EvidenceItem `json:"evidence"`
	Repair   map[string]any `json:"repair"` // empty in P1, populated in P3
}

// Subject identifies the entity the finding is about (selector resolution).
type Subject struct {
	EntityKind string `json:"entity_kind"`
	EntityID   string `json:"entity_id"`
	Qualified  string `json:"qualified_name"`
	Flow       string `json:"flow,omitempty"`
}

// EvidenceItem documents one piece of evidence supporting the finding.
type EvidenceItem struct {
	Kind   string `json:"kind"`   // e.g. "flow_touched_without_ack"
	Detail string `json:"detail"` // free-form English
}

// ValidateDiffResult summarizes a single validate-diff invocation.
type ValidateDiffResult struct {
	ValidationSeq uint64              `json:"validation_seq"`
	DiffSHA       string              `json:"diff_sha"`
	Findings      []ValidationFinding `json:"findings"`
	Summary       string              `json:"summary"`
}

// Pipeline wires together the kernel state required to validate a diff.
//
// Resolver and Events are optional in P0 — when nil the pipeline still
// produces flow_unreviewed findings exactly like the Phase 0 path, just
// without the new P1 finding kinds. Production wiring (CLI / daemon /
// MCP) supplies both so unresolved_anchor and symbol_disambiguation
// findings surface alongside.
type Pipeline struct {
	Overlay  *semantic_overlay.Overlay
	Code     *code_core.Store
	Resolver *semantic_overlay.Resolver // P1.G unresolved_anchor source-of-truth
	Events   *facts.EventLog            // P1.G symbol_disambiguation source-of-truth
}

// ValidateDiff runs all 12 stages against the unified diff in `unified` and
// returns the structured result. Idempotent at fixed seq.
func (p *Pipeline) ValidateDiff(ctx context.Context, unified []byte, validationSeq uint64) (*ValidateDiffResult, error) {
	res := &ValidateDiffResult{
		ValidationSeq: validationSeq,
		DiffSHA:       hashBytes(unified),
		Findings:      []ValidationFinding{},
	}

	// Stage 1: parse diff → hunks per file.
	hunks := parseUnifiedDiff(unified)
	if len(hunks) == 0 {
		res.Summary = "0 findings (empty diff)"
		return res, nil
	}

	// Stage 2: map hunks → code.core entities (P0 = qualified-name lookup
	// against modified function lines). Production resolution requires
	// tree-sitter range overlap (P0.T29 fully); the qualified-name path
	// covers the demo end-to-end and graduates as the indexer matures.
	touched := map[string]string{} // qualified_name -> entity_id
	for _, h := range hunks {
		for _, qn := range extractFunctionNames(h.AddedLines) {
			ent, err := p.Code.LookupByQualifiedNameSuffix(ctx, qn)
			if err != nil {
				return nil, err
			}
			if ent != nil {
				touched[ent.QualifiedName] = ent.ID
			}
		}
	}

	// Stages 3, 3b: bounded refresh + pin validation_seq — implicit at the
	// validation_seq input here. We honor the contract.
	// Stages 4, 5: touched_set + impacted_set (P0 = touched only; impacted
	// requires call-graph edges that arrive in P1 via LSP/SCIP).
	// Stage 6: resolve flow selectors against touched.
	for flowName, flow := range p.Overlay.Flows {
		// In P0 we resolve the flow's scope as a selector by name.
		scope := flow.Scope
		if scope == "" {
			continue
		}
		env, err := p.Overlay.Resolve(ctx, scope, p.Code, validationSeq)
		if err != nil {
			continue
		}
		for _, m := range env.Matches {
			if _, hit := touched[m.QualifiedName]; !hit {
				continue
			}
			// Stage 7-9: invariant/control/skill checks (pass-through in P0).
			// Stage 10: emit the finding.
			f := ValidationFinding{
				ID:       newFindingID(res.DiffSHA, flowName, m.QualifiedName),
				Kind:     "flow_unreviewed",
				Severity: "medium",
				Subject: Subject{
					EntityKind: "code.core:Function",
					EntityID:   m.EntityID,
					Qualified:  m.QualifiedName,
					Flow:       flowName,
				},
				Evidence: []EvidenceItem{{
					Kind:   "flow_touched_without_ack",
					Detail: fmt.Sprintf("touched flow-scoped function %s without acknowledging flow %s", m.QualifiedName, flowName),
				}},
				Repair: map[string]any{}, // empty in P0; P3 populates
			}
			res.Findings = append(res.Findings, f)
		}
	}

	// Stage 6 (P1.G): surface code.core.SymbolDisambiguation events as a
	// `symbol_disambiguation` finding so reviewers see conflicting source
	// claims inline.
	disFindings, err := p.collectSymbolDisambiguationFindings(ctx, res.DiffSHA, validationSeq)
	if err != nil {
		return nil, err
	}
	res.Findings = append(res.Findings, disFindings...)

	// Stage 10 (P1.G): emit `unresolved_anchor` for any overlay selector
	// that was bound (or reanchored) at validation_seq − 1 and is now
	// unresolved.
	uaFindings, err := p.collectUnresolvedAnchorFindings(ctx, res.DiffSHA, validationSeq)
	if err != nil {
		return nil, err
	}
	res.Findings = append(res.Findings, uaFindings...)

	// Stage 11: pass-through. Stage 12: emit the summary.
	res.Summary = fmt.Sprintf("%d findings", len(res.Findings))
	return res, nil
}

// collectSymbolDisambiguationFindings reads code.core.SymbolDisambiguation
// events in the log up through validation_seq and emits one finding per
// event. The event's payload (per the unifier) carries the canonical key
// each source claimed; the finding surfaces those claims to the reviewer.
//
// Pipelines without a wired EventLog return no findings — the stage
// degrades cleanly so existing P0 callers don't change behavior.
func (p *Pipeline) collectSymbolDisambiguationFindings(ctx context.Context, diffSHA string, validationSeq uint64) ([]ValidationFinding, error) {
	if p.Events == nil {
		return nil, nil
	}
	events, err := p.Events.ReadAsOf(ctx, validationSeq)
	if err != nil {
		return nil, err
	}
	var out []ValidationFinding
	for _, ev := range events {
		if ev.Layer != "code.core" || ev.Kind != "SymbolDisambiguation" {
			continue
		}
		out = append(out, symbolDisambiguationFinding(diffSHA, ev))
	}
	return out, nil
}

func symbolDisambiguationFinding(diffSHA string, ev kernel.Event) ValidationFinding {
	subj := Subject{EntityKind: "code.core:Symbol"}
	if ev.Subject != nil {
		subj.EntityID = ev.Subject.ID
	}
	// Decode the unifier's payload best-effort to surface per-source
	// canonical-key claims; on parse failure fall back to the raw payload.
	detail := string(ev.Payload)
	var parsed struct {
		QualifiedName string `json:"qualified_name"`
		Sources       []struct {
			SourceClass string `json:"source_class"`
			ProducedBy  string `json:"produced_by"`
			Signature   string `json:"signature,omitempty"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(ev.Payload, &parsed); err == nil {
		if parsed.QualifiedName != "" {
			subj.Qualified = parsed.QualifiedName
		}
		if len(parsed.Sources) > 0 {
			parts := make([]string, 0, len(parsed.Sources))
			for _, s := range parsed.Sources {
				if s.Signature != "" {
					parts = append(parts, fmt.Sprintf("%s claims sig=%q", s.SourceClass, s.Signature))
				} else {
					parts = append(parts, fmt.Sprintf("%s (%s)", s.SourceClass, s.ProducedBy))
				}
			}
			detail = strings.Join(parts, "; ")
		}
	}
	return ValidationFinding{
		ID:       newSymbolDisambiguationID(diffSHA, ev.Seq),
		Kind:     "symbol_disambiguation",
		Severity: "medium",
		Subject:  subj,
		Evidence: []EvidenceItem{{
			Kind:   "source_disagreement",
			Detail: detail,
		}},
		Repair: map[string]any{},
	}
}

// collectUnresolvedAnchorFindings walks every overlay selector and emits
// an `unresolved_anchor` finding when the *current* resolution at
// validation_seq is unresolved AND the cache holds a prior bound /
// reanchored result for the same selector AST. The pipeline thereby
// surfaces "this diff broke a binding" without needing to re-run the
// resolver against an as-of-seq snapshot of code.core.
func (p *Pipeline) collectUnresolvedAnchorFindings(ctx context.Context, diffSHA string, validationSeq uint64) ([]ValidationFinding, error) {
	if p.Resolver == nil {
		return nil, nil
	}
	var out []ValidationFinding
	for name := range p.Overlay.Selectors {
		// Capture prior outcome BEFORE the new Resolve call overwrites the
		// cache entry for this selector. The cache retains only the latest
		// resolution per selector_key, so reading priorAt second would lose
		// the seq-1 record we want to compare against.
		envPrior, hasPrior, err := p.Resolver.PriorAt(ctx, name, validationSeq)
		if err != nil {
			return nil, err
		}
		envCur, _, err := p.Resolver.Resolve(ctx, name, validationSeq)
		if err != nil {
			continue
		}
		if envCur.Outcome != semantic_overlay.OutcomeUnresolved {
			continue
		}
		if !hasPrior {
			continue
		}
		switch envPrior.Outcome {
		case semantic_overlay.OutcomeBound, semantic_overlay.OutcomeReanchored:
			out = append(out, unresolvedAnchorFinding(diffSHA, name, envPrior))
		}
	}
	return out, nil
}

func unresolvedAnchorFinding(diffSHA, selector string, prior *semantic_overlay.ResolutionEnvelope) ValidationFinding {
	subj := Subject{
		EntityKind: "semantic.overlay:Selector",
		EntityID:   selector,
	}
	if len(prior.Matches) > 0 {
		subj.Qualified = prior.Matches[0].QualifiedName
	}
	priorSummary := fmt.Sprintf("previously %s", prior.Outcome)
	if len(prior.Matches) > 0 {
		priorSummary = fmt.Sprintf("previously %s via %s (entity=%s)",
			prior.Outcome, prior.Matches[0].ViaAnchor, prior.Matches[0].QualifiedName)
	}
	return ValidationFinding{
		ID:       newUnresolvedAnchorID(diffSHA, selector),
		Kind:     "unresolved_anchor",
		Severity: "high",
		Subject:  subj,
		Evidence: []EvidenceItem{{
			Kind:   "binding_lost",
			Detail: fmt.Sprintf("selector %q is unresolved at this validation_seq; %s", selector, priorSummary),
		}},
		Repair: map[string]any{},
	}
}

func newSymbolDisambiguationID(diffSHA string, seq uint64) string {
	h := sha256.New()
	_, _ = h.Write([]byte(diffSHA))
	_, _ = h.Write([]byte{0})
	_, _ = fmt.Fprintf(h, "sd:%d", seq)
	return "finding_" + hex.EncodeToString(h.Sum(nil))[:8]
}

func newUnresolvedAnchorID(diffSHA, selector string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(diffSHA))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte("ua:"))
	_, _ = h.Write([]byte(selector))
	return "finding_" + hex.EncodeToString(h.Sum(nil))[:8]
}

// Hunk is a single hunk extracted from a unified diff, keyed by file path.
type Hunk struct {
	Path       string
	AddedLines []string
}

var hunkHeaderRE = regexp.MustCompile(`^@@`)

func parseUnifiedDiff(b []byte) []Hunk {
	lines := strings.Split(string(b), "\n")
	var hunks []Hunk
	var current Hunk
	flush := func() {
		if current.Path != "" || len(current.AddedLines) > 0 {
			hunks = append(hunks, current)
		}
		current = Hunk{}
	}
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "+++ "):
			flush()
			current.Path = strings.TrimPrefix(strings.TrimSpace(line), "+++ ")
			// Strip the leading "b/" prefix git emits.
			current.Path = strings.TrimPrefix(current.Path, "b/")
		case hunkHeaderRE.MatchString(line):
			// Hunk delimiter; keep accumulating into current.
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			current.AddedLines = append(current.AddedLines, strings.TrimPrefix(line, "+"))
		}
	}
	flush()
	return hunks
}

// extractFunctionNames grabs `func Name(` and `func (recv *T) Method(`
// signatures out of added lines. The names returned are package-less
// (resolution against code.core handles namespacing via the qualified_name
// column, which preserves package prefix when the file was ingested).
//
// In P0 the matcher operates by suffix: the qualified_name column carries
// `pkg.Name` or `pkg.Recv.Name`, so we match on the trailing component.
var (
	funcRE   = regexp.MustCompile(`^\s*func\s+([A-Z][A-Za-z0-9_]*)\s*\(`)
	methodRE = regexp.MustCompile(`^\s*func\s+\([^)]*\)\s+([A-Z][A-Za-z0-9_]*)\s*\(`)
)

func extractFunctionNames(addedLines []string) []string {
	out := []string{}
	for _, l := range addedLines {
		if m := methodRE.FindStringSubmatch(l); m != nil {
			out = append(out, m[1])
			continue
		}
		if m := funcRE.FindStringSubmatch(l); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])[:16]
}

func newFindingID(diffSha, flowName, qn string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(diffSha + "\x00" + flowName + "\x00" + qn))
	return "finding_" + hex.EncodeToString(h.Sum(nil))[:8]
}
