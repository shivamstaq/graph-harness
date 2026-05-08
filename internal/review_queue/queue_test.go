package review_queue

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// openTestQueue spins up an in-memory SQLite-backed Queue for tests.
func openTestQueue(t *testing.T) *Queue {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	q, err := NewQueue(db)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	return q
}

func TestSubmit_NoRequirements_GoesPendingReview(t *testing.T) {
	q := openTestQueue(t)
	id, err := q.Submit(context.Background(), Proposal{
		TargetLayer: "semantic.overlay",
		Kind:        "selector",
		Author:      "agent:test",
		Payload:     []byte(`{"name":"X"}`),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got, err := q.Get(context.Background(), id)
	if err != nil || got == nil {
		t.Fatalf("Get: %v / %v", got, err)
	}
	if got.State != StatePendingReview {
		t.Errorf("state = %s, want %s", got.State, StatePendingReview)
	}
}

func TestLoadRequirements_Parses(t *testing.T) {
	q := openTestQueue(t)
	manifest := []byte(`
proposal_evidence_requirements:
  invariant_proposal:
    required:
      - { kind: symbol,          min_count: 1 }
      - { kind: test_or_pattern, min_count: 1 }
    description_min_length: 50
  flow_proposal:
    required:
      - { kind: entry_symbol }
      - { kind: at_least_one_step }
  anchor_update:
    required:
      - { kind: drift_signal }
      - { kind: candidate_match, min_confidence: 0.7 }
`)
	if err := q.LoadRequirements(manifest); err != nil {
		t.Fatalf("LoadRequirements: %v", err)
	}
	r, ok := q.Requirements("invariant_proposal")
	if !ok {
		t.Fatalf("invariant_proposal requirements not loaded")
	}
	if r.DescriptionMinLength != 50 {
		t.Errorf("description_min_length = %d, want 50", r.DescriptionMinLength)
	}
	if len(r.Required) != 2 {
		t.Errorf("required entries = %d, want 2", len(r.Required))
	}
	if a, ok := q.Requirements("anchor_update"); !ok ||
		len(a.Required) != 2 || a.Required[1].MinConfidence != 0.7 {
		t.Errorf("anchor_update requirements = %+v", a)
	}
}

func TestSubmit_MissingSymbolEvidence_GoesNeedsEvidence(t *testing.T) {
	q := openTestQueue(t)
	if err := q.LoadRequirements([]byte(`
proposal_evidence_requirements:
  invariant_proposal:
    required:
      - { kind: symbol,          min_count: 1 }
      - { kind: test_or_pattern, min_count: 1 }
    description_min_length: 50
`)); err != nil {
		t.Fatalf("LoadRequirements: %v", err)
	}
	id, err := q.Submit(context.Background(), Proposal{
		TargetLayer: "semantic.overlay",
		Kind:        "invariant_proposal",
		Author:      "agent:claude",
		Description: strings.Repeat("x", 60), // satisfies description_min_length
		Payload:     []byte(`{}`),
		Evidence:    []EvidenceItem{{Kind: "test", Detail: "tests/foo_test.go"}},
		// missing symbol evidence
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got, _ := q.Get(context.Background(), id)
	if got.State != StateNeedsEvidence {
		t.Fatalf("state = %s, want %s; missing=%v", got.State, StateNeedsEvidence, got.MissingItems)
	}
}

func TestSubmit_ShortDescription_GoesNeedsEvidence(t *testing.T) {
	q := openTestQueue(t)
	_ = q.LoadRequirements([]byte(`
proposal_evidence_requirements:
  invariant_proposal:
    required:
      - { kind: symbol, min_count: 1 }
    description_min_length: 50
`))
	id, _ := q.Submit(context.Background(), Proposal{
		TargetLayer: "semantic.overlay",
		Kind:        "invariant_proposal",
		Description: "too short",
		Evidence:    []EvidenceItem{{Kind: "symbol", Detail: "Foo.Bar"}},
	})
	got, _ := q.Get(context.Background(), id)
	if got.State != StateNeedsEvidence {
		t.Errorf("expected needs_evidence due to short description, got %s", got.State)
	}
}

func TestSubmit_AllRequirementsMet_GoesPendingReview(t *testing.T) {
	q := openTestQueue(t)
	_ = q.LoadRequirements([]byte(`
proposal_evidence_requirements:
  invariant_proposal:
    required:
      - { kind: symbol,          min_count: 1 }
      - { kind: test_or_pattern, min_count: 1 }
    description_min_length: 10
`))
	id, _ := q.Submit(context.Background(), Proposal{
		Kind:        "invariant_proposal",
		Description: "Auth must be checked before any payment mutation.",
		Evidence: []EvidenceItem{
			{Kind: "symbol", Detail: "PaymentService.authorize"},
			{Kind: "test", Detail: "tests/payment_test.go::TestAuth"},
		},
	})
	got, _ := q.Get(context.Background(), id)
	if got.State != StatePendingReview {
		t.Errorf("state = %s, want %s", got.State, StatePendingReview)
	}
}

func TestAddEvidence_PromotesNeedsEvidenceToPendingReview(t *testing.T) {
	q := openTestQueue(t)
	_ = q.LoadRequirements([]byte(`
proposal_evidence_requirements:
  anchor_update:
    required:
      - { kind: drift_signal }
      - { kind: candidate_match, min_confidence: 0.7 }
`))
	ctx := context.Background()
	id, _ := q.Submit(ctx, Proposal{
		Kind:     "anchor_update",
		Evidence: []EvidenceItem{{Kind: "drift_signal", Detail: "rename"}},
	})
	if got, _ := q.Get(ctx, id); got.State != StateNeedsEvidence {
		t.Fatalf("pre: state = %s, want %s", got.State, StateNeedsEvidence)
	}
	// Add a candidate_match below threshold first — should still be needs_evidence.
	st, err := q.AddEvidence(ctx, id, []EvidenceItem{{Kind: "candidate_match", Confidence: 0.5}})
	if err != nil {
		t.Fatalf("AddEvidence (low conf): %v", err)
	}
	if st != StateNeedsEvidence {
		t.Errorf("low-conf AddEvidence transitioned to %s, want %s", st, StateNeedsEvidence)
	}
	// Add a stronger candidate_match — should auto-promote.
	st, err = q.AddEvidence(ctx, id, []EvidenceItem{{Kind: "candidate_match", Confidence: 0.85}})
	if err != nil {
		t.Fatalf("AddEvidence (high conf): %v", err)
	}
	if st != StatePendingReview {
		t.Errorf("high-conf AddEvidence transitioned to %s, want %s", st, StatePendingReview)
	}
}

func TestSubmit_FlowProposal_AtLeastOneStep(t *testing.T) {
	q := openTestQueue(t)
	_ = q.LoadRequirements([]byte(`
proposal_evidence_requirements:
  flow_proposal:
    required:
      - { kind: entry_symbol }
      - { kind: at_least_one_step }
`))
	ctx := context.Background()
	// No steps in payload → needs_evidence.
	id, _ := q.Submit(ctx, Proposal{
		Kind:     "flow_proposal",
		Payload:  []byte(`{"name":"NoSteps"}`),
		Evidence: []EvidenceItem{{Kind: "entry_symbol", Detail: "X.start"}},
	})
	if got, _ := q.Get(ctx, id); got.State != StateNeedsEvidence {
		t.Errorf("expected needs_evidence, got %s", got.State)
	}
	// With a steps array → pending_review.
	id2, _ := q.Submit(ctx, Proposal{
		Kind:     "flow_proposal",
		Payload:  []byte(`{"name":"WithSteps","steps":[{"name":"Validate"}]}`),
		Evidence: []EvidenceItem{{Kind: "entry_symbol", Detail: "X.start"}},
	})
	if got, _ := q.Get(ctx, id2); got.State != StatePendingReview {
		t.Errorf("expected pending_review, got %s", got.State)
	}
}

func TestSubmit_HonorsExplicitState(t *testing.T) {
	// Importers / replay paths supply a fixed state that bypasses validation.
	q := openTestQueue(t)
	_ = q.LoadRequirements([]byte(`
proposal_evidence_requirements:
  invariant_proposal:
    required: [{ kind: symbol }]
`))
	id, _ := q.Submit(context.Background(), Proposal{
		Kind:  "invariant_proposal",
		State: StateAccepted, // missing evidence, but caller forced state
	})
	got, _ := q.Get(context.Background(), id)
	if got.State != StateAccepted {
		t.Errorf("state = %s, want %s (explicit caller state should be honored)", got.State, StateAccepted)
	}
}
