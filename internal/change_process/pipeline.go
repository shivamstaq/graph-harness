// Package change_process implements the 12-stage validation pipeline.
//
// Phase 1 + 2 emit five finding kinds:
//
//   - `flow_unreviewed` (stage 10) — touched flow-scoped function without
//     acknowledging the flow (Phase 0 carryover).
//   - `unresolved_anchor` (stage 10) — a selector that was bound at
//     validation_seq − 1 is unresolved at validation_seq, indicating
//     a code edit broke an authored binding without a fall-through anchor
//     catching it (SPEC §8.2 + plan §P1.T30).
//   - `selector_reanchored` (stage 10) — a selector whose primary anchor
//     missed but whose fingerprint fallback (function_signature, body_hash,
//     ast_hash, symbol_fingerprint, or call_neighborhood) re-bound the
//     selector at confidence ≥ thresh.reanchored. Surfaces "this rename
//     was caught by the anchor ladder" so reviewers see which fingerprint
//     paid for the rebind (plan §3 gate criterion 6).
//   - `symbol_disambiguation` (stage 6) — surfaces
//     `code.core.SymbolDisambiguation` events as a finding kind so the
//     reviewer sees the conflicting source claims inline (plan §P1.T31).
//   - `missing_dependent_update` (stage 6) — Phase 2 (P2.T36). Emitted when
//     a touched producer (`EventPublisher`, `SchemaField`, or `Route`)
//     has downstream dependents (subscribers, schema reads/writes,
//     contract tests, handlers) that are NOT also touched in the same
//     diff. Stage 5 walks framework edges to populate the impacted
//     set; stage 6 fires one finding per producer with the stale
//     dependents listed in evidence (plan §P2.T35–T37).
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
	"sort"
	"strings"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/semantic_overlay"
)

// FindingKindMissingDependentUpdate is the P2 finding emitted when a
// change to an EventPublisher / SchemaField / Route leaves a downstream
// entity (subscriber, contract test, schema read/write, route handler)
// untouched in the same diff. Evidence carries the list of dependents
// that should have been updated together. (plan §P2.T36)
const FindingKindMissingDependentUpdate = "missing_dependent_update"

// ValidationFinding is the SPEC §8.2 shape. The CLI emits this as JSON via
// `validate-diff --json`. P1 finding kinds: `flow_unreviewed`,
// `unresolved_anchor`, `symbol_disambiguation`. P2 adds
// `missing_dependent_update`.
type ValidationFinding struct {
	ID       string         `json:"id"`
	Kind     string         `json:"kind"`     // flow_unreviewed | unresolved_anchor | selector_reanchored | symbol_disambiguation | missing_dependent_update
	Severity string         `json:"severity"` // info | low | medium | high | critical
	Subject  Subject        `json:"subject"`
	Evidence []EvidenceItem `json:"evidence"`
	Repair   map[string]any `json:"repair"` // empty in P1/P2, populated in P3

	// FrameworkContext is the P2.T37 per-control-firing context that
	// surfaces alongside framework-edge findings. Populated for
	// `missing_dependent_update` and any future control whose subject
	// is a touched framework producer so the control evidence can
	// reference "you changed an event payload — here are the
	// subscribers" without a second lookup. Omitted from JSON when
	// the touched entity is not a framework producer.
	FrameworkContext *FrameworkContext `json:"framework_context,omitempty"`
}

// FrameworkContext is the structured payload that Stage 7 injects when
// a control's subject (or a Stage 6 finding's subject) is a touched
// framework producer. Carried by ValidationFinding so the consumer
// (Studio, TUI, MCP, control evidence templates) can render
// "publisher → dependents" without re-walking the impacted set.
type FrameworkContext struct {
	TouchedKind    string         `json:"touched_kind"` // "EventPublisher" | "SchemaField" | "Route"
	TouchedSubject EntityRef      `json:"touched_subject"`
	Dependents     []DependentRef `json:"dependents"`
}

// EntityRef is a compact reference to a code.core / code.framework entity.
type EntityRef struct {
	Kind          string `json:"kind"`
	ID            string `json:"id"`
	QualifiedName string `json:"qualified_name"`
}

// DependentRef is one stale dependent surfaced in FrameworkContext.
// Reason is a human-readable phrase ("subscribes to event",
// "reads field", "handles route") explaining why the dependent is
// considered impacted.
type DependentRef struct {
	Kind          string `json:"kind"`
	ID            string `json:"id"`
	QualifiedName string `json:"qualified_name"`
	Reason        string `json:"reason"`
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
		for _, qn := range extractFunctionNames(h.Path, h.AddedLines) {
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
	// Stage 4: touched_set is `touched` above. Expand it through the
	// reverse selector index so a touched Function that anchors a
	// framework producer (e.g. a `PublishOrder` Go func whose body
	// emits the `kafka:order.created` event) marks that producer as
	// touched too — otherwise the producer would itself appear as a
	// stale dependent in the impacted set (plan §P2.T35 "or anchors
	// to one via reverse index").
	touchedIDs, err := p.expandTouchedViaReverseIndex(ctx, touched)
	if err != nil {
		return nil, err
	}

	// Stage 5: impacted_set = touched ∪ framework-edge dependents.
	// Naive BFS per plan §P2.T35 — Mangle rule engine arrives in P3.
	// Capped at depth 4 to match the code.framework manifest's
	// `traversal.max_depth`.
	impacted, err := p.computeImpactedSet(ctx, touchedIDs)
	if err != nil {
		return nil, err
	}

	// Stage 6 (P2.T36): emit `missing_dependent_update` findings for any
	// framework producer that has dependents NOT in the touched set.
	mduFindings := p.collectMissingDependentUpdateFindings(res.DiffSHA, touchedIDs, impacted)
	res.Findings = append(res.Findings, mduFindings...)

	// Stage 6 (continued): resolve flow selectors against touched.
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

// ImpactedFromDiff is the P2.T41 reuse seam for surfaces that need the
// Stage 4 + 5 impacted set (touched entities ∪ framework dependents)
// WITHOUT the Stage 6+ finding-emission filtering. The MCP `impacted_flows`
// tool calls this so it can enumerate every producer reached from the
// diff — not just producers whose dependents are stale (which is what
// `missing_dependent_update` filters to).
//
// Returns:
//
//   - touchedQNs   — qualified-name → entity_id map for entities the diff
//     directly added text to (Stage 2 output).
//   - touchedIDs   — touched_set after Stage 4 reverse-index expansion.
//   - producers    — every framework producer reachable in ≤ 4 BFS hops,
//     each with its full dependent list (NOT filtered to
//     stale). The shape mirrors FrameworkContext so callers
//     can render "publisher → dependents" directly.
//
// The pipeline still walks via the private computeImpactedSet so the
// BFS rules stay single-sourced.
func (p *Pipeline) ImpactedFromDiff(ctx context.Context, unified []byte) (
	touchedQNs map[string]string,
	touchedIDs map[string]struct{},
	producers []FrameworkContext,
	err error,
) {
	hunks := parseUnifiedDiff(unified)
	touchedQNs = map[string]string{}
	for _, h := range hunks {
		for _, qn := range extractFunctionNames(h.Path, h.AddedLines) {
			ent, lerr := p.Code.LookupByQualifiedNameSuffix(ctx, qn)
			if lerr != nil {
				return nil, nil, nil, lerr
			}
			if ent != nil {
				touchedQNs[ent.QualifiedName] = ent.ID
			}
		}
	}
	touchedIDs, err = p.expandTouchedViaReverseIndex(ctx, touchedQNs)
	if err != nil {
		return nil, nil, nil, err
	}
	impacted, err := p.computeImpactedSet(ctx, touchedIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	// Stable order: same key fn as sortProducers.
	recs := make([]*impactedRecord, 0, len(impacted))
	for _, r := range impacted {
		recs = append(recs, r)
	}
	sortProducers(recs)
	producers = make([]FrameworkContext, 0, len(recs))
	for _, r := range recs {
		producers = append(producers, FrameworkContext{
			TouchedKind: r.ProducerKind,
			TouchedSubject: EntityRef{
				Kind:          r.ProducerKind,
				ID:            r.ProducerID,
				QualifiedName: r.ProducerQN,
			},
			Dependents: append([]DependentRef(nil), r.Dependents...),
		})
	}
	return touchedQNs, touchedIDs, producers, nil
}

// ImpactedFromTouched is the selector-driven sibling of ImpactedFromDiff.
// Callers that have already projected a named selector to a set of
// touched entity IDs use this to walk the same Stage-4 + Stage-5 BFS.
// Returns the post-reverse-index touched set + every producer reached.
func (p *Pipeline) ImpactedFromTouched(ctx context.Context, seedEntityIDs []string) (
	touchedIDs map[string]struct{},
	producers []FrameworkContext,
	err error,
) {
	seed := map[string]string{}
	for _, id := range seedEntityIDs {
		seed[id] = id // expandTouchedViaReverseIndex only uses values
	}
	touchedIDs, err = p.expandTouchedViaReverseIndex(ctx, seed)
	if err != nil {
		return nil, nil, err
	}
	impacted, err := p.computeImpactedSet(ctx, touchedIDs)
	if err != nil {
		return nil, nil, err
	}
	recs := make([]*impactedRecord, 0, len(impacted))
	for _, r := range impacted {
		recs = append(recs, r)
	}
	sortProducers(recs)
	producers = make([]FrameworkContext, 0, len(recs))
	for _, r := range recs {
		producers = append(producers, FrameworkContext{
			TouchedKind: r.ProducerKind,
			TouchedSubject: EntityRef{
				Kind:          r.ProducerKind,
				ID:            r.ProducerID,
				QualifiedName: r.ProducerQN,
			},
			Dependents: append([]DependentRef(nil), r.Dependents...),
		})
	}
	return touchedIDs, producers, nil
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

// collectUnresolvedAnchorFindings walks every overlay selector and emits:
//
//   - `unresolved_anchor`   — current resolution is unresolved AND the cache
//     holds a prior bound / reanchored result for the
//     same selector AST. Surfaces "this diff broke a
//     binding" (plan §P1.T30).
//   - `selector_reanchored` — current resolution is reanchored at confidence
//     ≥ thresh.reanchored. Surfaces "this selector
//     survived a rename via fingerprint fallback"
//     (plan §3 gate criterion 6). Fires every time
//     reanchor wins, regardless of prior cache state,
//     so a fresh CI run on a renamed function
//     produces the signal.
//
// Both share the same prior-aware resolver path; capturing the prior outcome
// BEFORE re-running Resolve is essential because the cache retains only the
// latest resolution per selector_key.
func (p *Pipeline) collectUnresolvedAnchorFindings(ctx context.Context, diffSHA string, validationSeq uint64) ([]ValidationFinding, error) {
	if p.Resolver == nil {
		return nil, nil
	}
	var out []ValidationFinding
	for name := range p.Overlay.Selectors {
		envPrior, hasPrior, err := p.Resolver.PriorAt(ctx, name, validationSeq)
		if err != nil {
			return nil, err
		}
		envCur, _, err := p.Resolver.Resolve(ctx, name, validationSeq)
		if err != nil {
			continue
		}
		switch envCur.Outcome {
		case semantic_overlay.OutcomeUnresolved:
			if !hasPrior {
				continue
			}
			if envPrior.Outcome == semantic_overlay.OutcomeBound ||
				envPrior.Outcome == semantic_overlay.OutcomeReanchored {
				out = append(out, unresolvedAnchorFinding(diffSHA, name, envPrior))
			}
		case semantic_overlay.OutcomeReanchored:
			out = append(out, selectorReanchoredFinding(diffSHA, name, envCur, envPrior))
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

// selectorReanchoredFinding emits when a selector resolves to outcome
// `reanchored` at the current validation_seq (gate criterion 6). The
// finding's evidence carries the via_anchor and confidence so reviewers
// see which fingerprint anchor caught the rename. When a prior envelope
// is available, the evidence also describes the bound→reanchored
// transition.
func selectorReanchoredFinding(diffSHA, selector string, cur *semantic_overlay.ResolutionEnvelope, prior *semantic_overlay.ResolutionEnvelope) ValidationFinding {
	subj := Subject{
		EntityKind: "semantic.overlay:Selector",
		EntityID:   selector,
	}
	via := ""
	conf := 0.0
	if len(cur.Matches) > 0 {
		subj.Qualified = cur.Matches[0].QualifiedName
		via = cur.Matches[0].ViaAnchor
		conf = cur.Matches[0].Confidence
	}
	transition := "(no prior cache entry)"
	if prior != nil {
		transition = fmt.Sprintf("transition: %s → reanchored", prior.Outcome)
	}
	return ValidationFinding{
		ID:       newSelectorReanchoredID(diffSHA, selector),
		Kind:     "selector_reanchored",
		Severity: "medium",
		Subject:  subj,
		Evidence: []EvidenceItem{{
			Kind: "fingerprint_fallback",
			Detail: fmt.Sprintf("outcome=reanchored via anchor=%s at confidence=%.2f; %s",
				via, conf, transition),
		}},
		Repair: map[string]any{},
	}
}

func newSelectorReanchoredID(diffSHA, selector string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(diffSHA))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte("sr:"))
	_, _ = h.Write([]byte(selector))
	return "finding_" + hex.EncodeToString(h.Sum(nil))[:8]
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

// frameworkProducerKinds enumerates the Kind values whose changes
// propagate across framework edges per plan §P2.T35. The pipeline
// fires a `missing_dependent_update` finding when a producer is
// touched but at least one of its downstream dependents is not.
//
// Convention (Pass-0.5-A): framework entities live in the same
// code_core.Store as code.core entities. The Kind column carries the
// framework kind verbatim ("Route" / "EventPublisher" / "SchemaField"
// / etc.). The qualified_name column carries the framework's shared
// linking key so producers and their dependents discover one another
// via `Store.LookupAllByQualifiedName(qn)`:
//
//   - Event family (publisher, subscriber, contract-test-for-event)
//     share `qualified_name = "<transport>:<event_name>"`
//     (e.g. "kafka:order.created").
//   - Schema-field family (field, schema-read, schema-write,
//     test-for-field) share
//     `qualified_name = "<schema_table>.<field_name>"`
//     (e.g. "users.email").
//   - Route family (route, handler, contract-test-for-route) share
//     `qualified_name = "<method> <path>"` (e.g. "GET /users/{id}").
//
// Per-extractor conventions are documented in plan/02-framework-extractors.md
// §P2.T15 / §P2.T22 / §P2.T09; the pipeline only needs the shared
// linking key to enumerate dependents.
var frameworkProducerKinds = map[string][]string{
	"EventPublisher": {"EventSubscriber", "ContractTest", "EventPublisher"},
	"SchemaField":    {"SchemaRead", "SchemaWrite", "Test", "SchemaField"},
	"Route":          {"Handler", "ContractTest", "Route"},
}

// dependentReason maps (producer_kind, dependent_kind) to a short
// English phrase used in FrameworkContext.Dependents[].Reason. The
// rendering keeps the per-finding evidence stable across runs (the
// pipeline's idempotence contract — SPEC §8.1).
var dependentReason = map[string]map[string]string{
	"EventPublisher": {
		"EventSubscriber": "subscribes to event",
		"ContractTest":    "contract test for event",
		"EventPublisher":  "co-publishes event",
	},
	"SchemaField": {
		"SchemaRead":  "reads field",
		"SchemaWrite": "writes field",
		"Test":        "tests field",
		"SchemaField": "co-defines field",
	},
	"Route": {
		"Handler":      "handles route",
		"ContractTest": "contract test for route",
		"Route":        "co-defines route",
	},
}

// impactedRecord is one row of the Stage-5 impacted set. Producers and
// dependents share the record shape so Stage-6 emission and Stage-7
// context injection can walk a single slice.
type impactedRecord struct {
	ProducerID   string         // touched entity that pulled this row in
	ProducerKind string         // "EventPublisher" / "SchemaField" / "Route"
	ProducerQN   string         // shared linking key
	Dependents   []DependentRef // every dependent reachable from the producer
}

// expandTouchedViaReverseIndex walks the selector reverse index from
// every diff-touched entity and adds any co-bound entity (typically a
// framework producer such as EventPublisher / SchemaField / Route) to
// the touched set. This is the "or anchors to one via reverse index"
// arm of plan §P2.T35: a touched Function that anchors a producer
// counts as touching the producer itself, otherwise the producer
// would show up as a stale dependent in its own
// `missing_dependent_update` finding.
//
// Uses the O(1) reverse index (Store.SelectorsBoundTo +
// Store.EntitiesForSelector) — naive fan-out scans are a non-goal
// per plan §5 risks.
func (p *Pipeline) expandTouchedViaReverseIndex(ctx context.Context, touched map[string]string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	for _, id := range touched {
		out[id] = struct{}{}
	}
	if p.Code == nil {
		return out, nil
	}
	// Snapshot the starting set so we don't iterate while mutating.
	seeds := make([]string, 0, len(out))
	for id := range out {
		seeds = append(seeds, id)
	}
	for _, id := range seeds {
		bindings, err := p.Code.SelectorsBoundTo(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, b := range bindings {
			peers, err := p.Code.EntitiesForSelector(ctx, b.SelectorID)
			if err != nil {
				return nil, err
			}
			for _, peer := range peers {
				out[peer] = struct{}{}
			}
		}
	}
	return out, nil
}

// computeImpactedSet is Stage 5 (P2.T35). For each touched entity it:
//
//  1. Looks up the full entity row to read its Kind + QualifiedName.
//  2. For every entity whose Kind is a framework producer,
//     enumerates dependents via shared qualified_name + producer-kind
//     dependent table.
//
// BFS is depth-capped at 4 (the manifest's
// `traversal.max_depth` for code.framework). Frontiers beyond depth 4
// are silently dropped — this is a naive walk, intentionally; the
// Mangle rule engine in P3 will replace it with a planned traversal.
//
// Reverse-index expansion is performed upstream by
// expandTouchedViaReverseIndex so the touched set passed in here
// already includes any framework producer co-bound to a diff-touched
// function.
//
// Returns the map producer_entity_id → impactedRecord. The producer
// is the touched entity that originated each record so Stage 6 can
// fire one finding per producer.
func (p *Pipeline) computeImpactedSet(ctx context.Context, touchedIDs map[string]struct{}) (map[string]*impactedRecord, error) {
	const maxDepth = 4 // code.framework manifest traversal.max_depth
	out := map[string]*impactedRecord{}
	if p.Code == nil {
		return out, nil
	}

	// Walk the touched set looking for framework producers.
	visited := map[string]struct{}{}
	frontier := make([]string, 0, len(touchedIDs))
	for id := range touchedIDs {
		frontier = append(frontier, id)
		visited[id] = struct{}{}
	}

	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, id := range frontier {
			ent, err := p.Code.LookupEntityByID(ctx, id)
			if err != nil {
				return nil, err
			}
			if ent == nil {
				continue
			}
			depKinds, isProducer := frameworkProducerKinds[string(ent.Kind)]
			if !isProducer || ent.QualifiedName == "" {
				continue
			}
			// Enumerate dependents via shared qualified_name.
			peers, err := p.Code.LookupAllByQualifiedName(ctx, ent.QualifiedName)
			if err != nil {
				return nil, err
			}
			rec, ok := out[ent.ID]
			if !ok {
				rec = &impactedRecord{
					ProducerID:   ent.ID,
					ProducerKind: string(ent.Kind),
					ProducerQN:   ent.QualifiedName,
				}
				out[ent.ID] = rec
			}
			for _, peer := range peers {
				if peer.ID == ent.ID {
					continue
				}
				if !containsString(depKinds, string(peer.Kind)) {
					continue
				}
				reason := dependentReason[string(ent.Kind)][string(peer.Kind)]
				rec.Dependents = append(rec.Dependents, DependentRef{
					Kind:          string(peer.Kind),
					ID:            peer.ID,
					QualifiedName: peer.QualifiedName,
					Reason:        reason,
				})
				if _, seen := visited[peer.ID]; !seen {
					visited[peer.ID] = struct{}{}
					next = append(next, peer.ID)
				}
			}
		}
		frontier = next
	}

	// Stable evidence ordering: sort each producer's dependents by
	// (Kind, QualifiedName, ID) so the pipeline's idempotence contract
	// holds at fixed kernel seq. Sort the producer map at emission
	// time (Stage 6) — the map itself is consumed by ID-keyed callers.
	for _, rec := range out {
		sortDependents(rec.Dependents)
	}
	return out, nil
}

// collectMissingDependentUpdateFindings is Stage 6 (P2.T36). For each
// producer in the impacted set, fires one finding listing every
// dependent NOT in the touched set. Severity per producer kind:
//
//   - EventPublisher → high (semantic-breakage risk)
//   - SchemaField    → high (data-shape break; addition is downgraded
//     to info when no readers/writers exist)
//   - Route          → medium
//
// Stage 7 (P2.T37) context injection: FrameworkContext is attached
// to every emitted finding so a downstream control (or surface) can
// reference the producer + stale-dependent set without re-walking
// the impacted map.
func (p *Pipeline) collectMissingDependentUpdateFindings(diffSHA string, touchedIDs map[string]struct{}, impacted map[string]*impactedRecord) []ValidationFinding {
	// Deterministic emission order: sort producers by (Kind, QN, ID).
	producers := make([]*impactedRecord, 0, len(impacted))
	for _, rec := range impacted {
		producers = append(producers, rec)
	}
	sortProducers(producers)

	var out []ValidationFinding
	for _, rec := range producers {
		// Filter dependents to the stale subset.
		stale := make([]DependentRef, 0, len(rec.Dependents))
		for _, d := range rec.Dependents {
			if _, hit := touchedIDs[d.ID]; hit {
				continue
			}
			stale = append(stale, d)
		}
		if len(stale) == 0 {
			continue
		}
		out = append(out, missingDependentUpdateFinding(diffSHA, rec, stale))
	}
	return out
}

func missingDependentUpdateFinding(diffSHA string, rec *impactedRecord, stale []DependentRef) ValidationFinding {
	subj := Subject{
		EntityKind: "code.framework:" + rec.ProducerKind,
		EntityID:   rec.ProducerID,
		Qualified:  rec.ProducerQN,
	}
	evidence := make([]EvidenceItem, 0, len(stale))
	for _, d := range stale {
		evidence = append(evidence, EvidenceItem{
			Kind:   FindingKindMissingDependentUpdate,
			Detail: d.Kind + ":" + d.QualifiedName,
		})
	}
	sev := severityForProducer(rec.ProducerKind, stale)
	return ValidationFinding{
		ID:       newMissingDependentUpdateID(diffSHA, rec.ProducerID),
		Kind:     FindingKindMissingDependentUpdate,
		Severity: sev,
		Subject:  subj,
		Evidence: evidence,
		Repair:   map[string]any{},
		FrameworkContext: &FrameworkContext{
			TouchedKind: rec.ProducerKind,
			TouchedSubject: EntityRef{
				Kind:          rec.ProducerKind,
				ID:            rec.ProducerID,
				QualifiedName: rec.ProducerQN,
			},
			Dependents: stale,
		},
	}
}

// severityForProducer applies the plan §P2.T36 severity mapping:
//
//   - EventPublisher → high (any subscriber drift breaks semantics)
//   - SchemaField    → high when reads or writes exist; info when the
//     only dependent kinds are co-defining SchemaField rows (an
//     addition with no consumers yet).
//   - Route          → medium.
//
// Unknown kinds fall through to "medium" (defensive default; the
// producer-kind table is closed in P2 but P3 will extend it).
func severityForProducer(kind string, stale []DependentRef) string {
	switch kind {
	case "EventPublisher":
		return "high"
	case "SchemaField":
		// "Addition" surfaces as a touched SchemaField with no
		// non-SchemaField dependents (no readers/writers). Demote
		// to info per plan §P2.T36.
		for _, d := range stale {
			if d.Kind != "SchemaField" {
				return "high"
			}
		}
		return "info"
	case "Route":
		return "medium"
	}
	return "medium"
}

func newMissingDependentUpdateID(diffSHA, producerID string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(diffSHA))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte("mdu:"))
	_, _ = h.Write([]byte(producerID))
	return "finding_" + hex.EncodeToString(h.Sum(nil))[:8]
}

func sortDependents(d []DependentRef) {
	sort.SliceStable(d, func(i, j int) bool {
		if d[i].Kind != d[j].Kind {
			return d[i].Kind < d[j].Kind
		}
		if d[i].QualifiedName != d[j].QualifiedName {
			return d[i].QualifiedName < d[j].QualifiedName
		}
		return d[i].ID < d[j].ID
	})
}

func sortProducers(p []*impactedRecord) {
	sort.SliceStable(p, func(i, j int) bool {
		if p[i].ProducerKind != p[j].ProducerKind {
			return p[i].ProducerKind < p[j].ProducerKind
		}
		if p[i].ProducerQN != p[j].ProducerQN {
			return p[i].ProducerQN < p[j].ProducerQN
		}
		return p[i].ProducerID < p[j].ProducerID
	})
}

func containsString(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
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

// extractFunctionNames pulls function/method declaration names out of the
// added lines of one diff hunk. Detection is dispatched by file extension
// derived from the hunk's path so a polyglot diff (Go + TypeScript +
// Python) is handled uniformly.
//
// The names returned are package-less; resolution against code.core
// happens via the qualified_name suffix lookup, so the trailing
// component is enough.
//
// Per-language patterns (deliberately lenient — false positives in
// `extractFunctionNames` are harmless because the suffix lookup against
// code.core only matches real entities):
//
//	go         — `func Name(`, `func (recv) Method(`
//	typescript — top-level `function`, `export function`, `async function`,
//	             arrow-bound `const Name = (…) =>`, class-method `Name(…) {`,
//	             also `static Name(`. Includes JS / JSX / TSX.
//	python     — `def name`, `async def name`. Class methods use the
//	             same `def` form so no separate pattern needed.
var (
	goFuncRE     = regexp.MustCompile(`^\s*func\s+([A-Z][A-Za-z0-9_]*)\s*\(`)
	goMethodRE   = regexp.MustCompile(`^\s*func\s+\([^)]*\)\s+([A-Z][A-Za-z0-9_]*)\s*\(`)
	tsFunctionRE = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][A-Za-z0-9_$]*)\s*[<(]`)
	tsArrowRE    = regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*(?::\s*[^=]+)?=\s*(?:async\s*)?\(`)
	tsMethodRE   = regexp.MustCompile(`^\s*(?:public\s+|private\s+|protected\s+|static\s+|async\s+|readonly\s+)*([A-Za-z_$][A-Za-z0-9_$]*)\s*(?:<[^>]*>)?\s*\([^)]*\)\s*(?::\s*[^{]+)?\s*\{`)
	pyFuncRE     = regexp.MustCompile(`^\s*(?:async\s+)?def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
)

// tsMethodReservedKeywords enumerates statement-leading keywords that
// would otherwise be matched by tsMethodRE's "ident(args) {" shape.
// Filtering them out keeps the false-positive rate sane on real diffs.
var tsMethodReservedKeywords = map[string]struct{}{
	"if": {}, "for": {}, "while": {}, "switch": {}, "catch": {},
	"return": {}, "throw": {}, "do": {}, "else": {}, "function": {},
	"new": {}, "typeof": {}, "in": {}, "of": {}, "await": {},
}

func extractFunctionNames(path string, addedLines []string) []string {
	switch detectLanguageFromPath(path) {
	case "go":
		return extractGoFunctionNames(addedLines)
	case "ts":
		return extractTSFunctionNames(addedLines)
	case "py":
		return extractPyFunctionNames(addedLines)
	}
	return nil
}

// detectLanguageFromPath maps a file path's extension to the same language
// IDs the rest of the code base uses (`go` / `ts` / `py`). Unknown
// extensions return the empty string and the diff hunk is skipped.
func detectLanguageFromPath(path string) string {
	switch {
	case strings.HasSuffix(path, ".go"):
		return "go"
	case strings.HasSuffix(path, ".ts"),
		strings.HasSuffix(path, ".tsx"),
		strings.HasSuffix(path, ".js"),
		strings.HasSuffix(path, ".jsx"),
		strings.HasSuffix(path, ".mjs"),
		strings.HasSuffix(path, ".cjs"):
		return "ts"
	case strings.HasSuffix(path, ".py"):
		return "py"
	}
	return ""
}

func extractGoFunctionNames(addedLines []string) []string {
	out := []string{}
	for _, l := range addedLines {
		if m := goMethodRE.FindStringSubmatch(l); m != nil {
			out = append(out, m[1])
			continue
		}
		if m := goFuncRE.FindStringSubmatch(l); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

func extractTSFunctionNames(addedLines []string) []string {
	out := []string{}
	for _, l := range addedLines {
		if m := tsFunctionRE.FindStringSubmatch(l); m != nil {
			out = append(out, m[1])
			continue
		}
		if m := tsArrowRE.FindStringSubmatch(l); m != nil {
			out = append(out, m[1])
			continue
		}
		if m := tsMethodRE.FindStringSubmatch(l); m != nil {
			name := m[1]
			if _, reserved := tsMethodReservedKeywords[name]; reserved {
				continue
			}
			out = append(out, name)
		}
	}
	return out
}

func extractPyFunctionNames(addedLines []string) []string {
	out := []string{}
	for _, l := range addedLines {
		if m := pyFuncRE.FindStringSubmatch(l); m != nil {
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
