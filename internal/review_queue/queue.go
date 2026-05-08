// Package review_queue implements the proposal lifecycle layer (SPEC §10).
// Phase 1 ships submitted | needs_evidence | pending_review | accepted |
// rejected | promoted; the remaining `superseded` / `accepted_with_edits` /
// `deferred` states stay deferred to P4. Per-proposal-kind evidence
// requirements (SPEC §10.4) are loaded from layer manifests at install
// time and enforced on Submit.
package review_queue

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// State is the P1-active subset of SPEC §10.1 states.
type State string

// Phase 1 review queue states. `superseded`, `accepted_with_edits`, and
// `deferred` remain P4-deferred.
const (
	StateSubmitted     State = "submitted"
	StateNeedsEvidence State = "needs_evidence"
	StatePendingReview State = "pending_review"
	StateAccepted      State = "accepted"
	StateRejected      State = "rejected"
	StatePromoted      State = "promoted"
)

// EvidenceItem is one piece of evidence attached to a proposal — the
// concrete shape (symbol pointer, test path, drift signal, candidate match)
// is identified by Kind and matched against the per-kind requirement set.
type EvidenceItem struct {
	Kind       string  `json:"kind"`
	Detail     string  `json:"detail,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// Proposal is one entry in the review queue.
type Proposal struct {
	ID           string         `json:"id"`
	TargetLayer  string         `json:"target_layer"`
	Kind         string         `json:"kind"` // invariant_proposal | flow_proposal | anchor_update | selector | flow | invariant ...
	Author       string         `json:"author"`
	State        State          `json:"state"`
	Payload      []byte         `json:"payload"` // canonical JSON AST of the change
	Evidence     []EvidenceItem `json:"evidence,omitempty"`
	Description  string         `json:"description,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	MissingItems []string       `json:"missing_evidence,omitempty"` // populated when state=needs_evidence
}

// EvidenceRequirement is one entry in a per-proposal-kind requirement set.
//
// SPEC §10.4 vocabulary for `kind`:
//
//	symbol             — at least N EvidenceItem records of kind=symbol
//	test_or_pattern    — at least N records of kind in {test, pattern}
//	entry_symbol       — exactly one EvidenceItem of kind=entry_symbol
//	at_least_one_step  — Payload JSON contains at least one `step` declaration
//	drift_signal       — at least one EvidenceItem of kind=drift_signal
//	candidate_match    — at least one EvidenceItem of kind=candidate_match with confidence ≥ MinConfidence
type EvidenceRequirement struct {
	Kind          string  `yaml:"kind"            json:"kind"`
	MinCount      int     `yaml:"min_count"       json:"min_count,omitempty"`
	MinConfidence float64 `yaml:"min_confidence"  json:"min_confidence,omitempty"`
}

// EvidenceRequirements is the per-proposal-kind requirement set.
type EvidenceRequirements struct {
	Required             []EvidenceRequirement `yaml:"required"               json:"required"`
	DescriptionMinLength int                   `yaml:"description_min_length" json:"description_min_length,omitempty"`
}

// Queue is the SQLite-backed review queue.
type Queue struct {
	db   *sql.DB
	mu   sync.RWMutex
	reqs map[string]EvidenceRequirements // proposal_kind -> requirements
}

// NewQueue wraps an opened *sql.DB and ensures the schema exists.
func NewQueue(db *sql.DB) (*Queue, error) {
	q := &Queue{db: db, reqs: map[string]EvidenceRequirements{}}
	const schema = `
CREATE TABLE IF NOT EXISTS review_proposals (
    id           TEXT PRIMARY KEY,
    target_layer TEXT NOT NULL,
    kind         TEXT NOT NULL,
    author       TEXT NOT NULL,
    state        TEXT NOT NULL,
    payload      BLOB,
    evidence     BLOB,
    description  TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_review_state ON review_proposals(state);
CREATE INDEX IF NOT EXISTS idx_review_layer ON review_proposals(target_layer);
`
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	// Phase 0 schema upgrade: best-effort ALTER for callers that opened a
	// pre-evidence database. SQLite ignores duplicate columns via the error
	// path; we treat any non-fatal error as already-applied.
	_, _ = db.Exec(`ALTER TABLE review_proposals ADD COLUMN evidence BLOB`)
	_, _ = db.Exec(`ALTER TABLE review_proposals ADD COLUMN description TEXT NOT NULL DEFAULT ''`)
	return q, nil
}

// LoadRequirements registers per-proposal-kind evidence requirements parsed
// from a layer manifest. The YAML shape is the SPEC §10.4 block:
//
//	proposal_evidence_requirements:
//	  invariant_proposal:
//	    required:
//	      - { kind: symbol, min_count: 1 }
//	    description_min_length: 50
//
// LoadRequirements is additive: calling it for multiple manifests merges the
// per-kind tables (later definitions overwrite earlier ones for the same
// proposal kind). Callers typically invoke this once per registered layer
// manifest at workspace install time.
func (q *Queue) LoadRequirements(manifestYAML []byte) error {
	var doc struct {
		ProposalEvidenceRequirements map[string]EvidenceRequirements `yaml:"proposal_evidence_requirements"`
	}
	if err := yaml.Unmarshal(manifestYAML, &doc); err != nil {
		return fmt.Errorf("review_queue: parse evidence requirements: %w", err)
	}
	if len(doc.ProposalEvidenceRequirements) == 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	maps.Copy(q.reqs, doc.ProposalEvidenceRequirements)
	return nil
}

// Requirements returns a copy of the registered requirement set for one
// proposal kind. The bool reports whether requirements were declared.
func (q *Queue) Requirements(proposalKind string) (EvidenceRequirements, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	r, ok := q.reqs[proposalKind]
	return r, ok
}

// Submit appends a proposal. Auto-validation against the loaded evidence
// requirements (SPEC §10.4) routes the proposal:
//
//   - all requirements satisfied → state=pending_review
//   - missing evidence            → state=needs_evidence, MissingItems populated
//
// Proposals whose Kind has no declared requirements skip validation and go
// straight to pending_review (preserves Phase 0 behavior).
//
// IDs are content-addressable (sha of layer + kind + payload) so duplicate
// submissions collapse.
func (q *Queue) Submit(ctx context.Context, p Proposal) (string, error) {
	if p.ID == "" {
		p.ID = newProposalID(p.TargetLayer, p.Kind, p.Payload)
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}

	missing := q.validateEvidence(p)
	switch {
	case p.State != "":
		// Honor the caller's explicit state (used by importers and replay).
	case len(missing) > 0:
		p.State = StateNeedsEvidence
		p.MissingItems = missing
	default:
		p.State = StatePendingReview
	}

	evJSON, _ := json.Marshal(p.Evidence)
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO review_proposals (id, target_layer, kind, author, state, payload, evidence, description, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING
	`, p.ID, p.TargetLayer, p.Kind, p.Author, string(p.State), p.Payload, evJSON, p.Description, p.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

// AddEvidence appends evidence items to an existing proposal. If the
// proposal was in `needs_evidence` and the new evidence (combined with any
// previously-attached items) satisfies the requirements, it is auto-
// promoted to `pending_review`. Returns the post-update state.
func (q *Queue) AddEvidence(ctx context.Context, id string, items []EvidenceItem) (State, error) {
	p, err := q.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if p == nil {
		return "", fmt.Errorf("proposal %s not found", id)
	}
	p.Evidence = append(p.Evidence, items...)
	missing := q.validateEvidence(*p)
	newState := p.State
	if p.State == StateNeedsEvidence && len(missing) == 0 {
		newState = StatePendingReview
	}
	evJSON, _ := json.Marshal(p.Evidence)
	res, err := q.db.ExecContext(ctx,
		`UPDATE review_proposals SET evidence = ?, state = ? WHERE id = ?`,
		evJSON, string(newState), id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", fmt.Errorf("proposal %s vanished mid-update", id)
	}
	return newState, nil
}

// validateEvidence returns the human-readable list of unmet requirements for
// proposal p. Empty slice == fully satisfied. Proposal kinds with no declared
// requirements always validate clean.
func (q *Queue) validateEvidence(p Proposal) []string {
	q.mu.RLock()
	req, ok := q.reqs[p.Kind]
	q.mu.RUnlock()
	if !ok {
		return nil
	}
	var missing []string
	for _, r := range req.Required {
		if msg := checkRequirement(r, p); msg != "" {
			missing = append(missing, msg)
		}
	}
	if req.DescriptionMinLength > 0 && len(p.Description) < req.DescriptionMinLength {
		missing = append(missing, fmt.Sprintf("description shorter than required %d chars (got %d)", req.DescriptionMinLength, len(p.Description)))
	}
	sort.Strings(missing)
	return missing
}

// checkRequirement returns a non-empty failure message when a single
// requirement is unmet against the proposal's evidence + payload.
func checkRequirement(r EvidenceRequirement, p Proposal) string {
	minCount := r.MinCount
	if minCount <= 0 {
		minCount = 1
	}
	switch r.Kind {
	case "symbol", "entry_symbol", "drift_signal", "test", "pattern":
		count := 0
		for _, e := range p.Evidence {
			if e.Kind == r.Kind {
				count++
			}
		}
		if count < minCount {
			return fmt.Sprintf("requires ≥%d evidence of kind=%s (got %d)", minCount, r.Kind, count)
		}
	case "test_or_pattern":
		count := 0
		for _, e := range p.Evidence {
			if e.Kind == "test" || e.Kind == "pattern" || e.Kind == "test_or_pattern" {
				count++
			}
		}
		if count < minCount {
			return fmt.Sprintf("requires ≥%d evidence of kind=test|pattern (got %d)", minCount, count)
		}
	case "candidate_match":
		ok := false
		for _, e := range p.Evidence {
			if e.Kind != "candidate_match" {
				continue
			}
			if e.Confidence >= r.MinConfidence {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Sprintf("requires candidate_match with confidence ≥%g", r.MinConfidence)
		}
	case "at_least_one_step":
		// Heuristic: payload JSON must mention a "step" key. Production
		// validators pass the parsed flow AST through here; the heuristic
		// keeps the SPEC §10.4 wording satisfiable for the CLI submit path.
		if !payloadDeclaresStep(p.Payload) {
			return "requires at least one flow step in payload"
		}
	default:
		// Unknown requirement kinds are left advisory: log via the missing
		// list so reviewers see them, but don't block.
		count := 0
		for _, e := range p.Evidence {
			if e.Kind == r.Kind {
				count++
			}
		}
		if count < minCount {
			return fmt.Sprintf("requires ≥%d evidence of kind=%s (got %d)", minCount, r.Kind, count)
		}
	}
	return ""
}

func payloadDeclaresStep(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return false
	}
	if v, ok := raw["steps"]; ok {
		if arr, ok := v.([]any); ok && len(arr) > 0 {
			return true
		}
	}
	if v, ok := raw["step"]; ok && v != nil {
		return true
	}
	return false
}

// List returns proposals optionally filtered by state.
func (q *Queue) List(ctx context.Context, stateFilter State) ([]Proposal, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if stateFilter == "" {
		rows, err = q.db.QueryContext(ctx,
			`SELECT id, target_layer, kind, author, state, payload, evidence, description, created_at
			 FROM review_proposals ORDER BY created_at ASC`)
	} else {
		rows, err = q.db.QueryContext(ctx,
			`SELECT id, target_layer, kind, author, state, payload, evidence, description, created_at
			 FROM review_proposals WHERE state = ? ORDER BY created_at ASC`, string(stateFilter))
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Proposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get returns a single proposal by ID.
func (q *Queue) Get(ctx context.Context, id string) (*Proposal, error) {
	row := q.db.QueryRowContext(ctx, `
		SELECT id, target_layer, kind, author, state, payload, evidence, description, created_at
		FROM review_proposals WHERE id = ?`, id)
	p, err := scanProposal(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// rowScanner abstracts *sql.Row and *sql.Rows for shared column scanning.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanProposal(rs rowScanner) (Proposal, error) {
	var p Proposal
	var st, ts string
	var ev []byte
	if err := rs.Scan(&p.ID, &p.TargetLayer, &p.Kind, &p.Author, &st, &p.Payload, &ev, &p.Description, &ts); err != nil {
		return Proposal{}, err
	}
	p.State = State(st)
	t, _ := time.Parse(time.RFC3339Nano, ts)
	p.CreatedAt = t
	if len(ev) > 0 {
		_ = json.Unmarshal(ev, &p.Evidence)
	}
	return p, nil
}

// Accept transitions a proposal to `accepted`.
func (q *Queue) Accept(ctx context.Context, id string) error {
	return q.transition(ctx, id, StateAccepted)
}

// Reject transitions a proposal to `rejected`.
func (q *Queue) Reject(ctx context.Context, id string) error {
	return q.transition(ctx, id, StateRejected)
}

// Promote transitions an accepted proposal to `promoted` (i.e. the change has
// been written into the target layer's canonical store).
func (q *Queue) Promote(ctx context.Context, id string) error {
	return q.transition(ctx, id, StatePromoted)
}

func (q *Queue) transition(ctx context.Context, id string, to State) error {
	res, err := q.db.ExecContext(ctx,
		`UPDATE review_proposals SET state = ? WHERE id = ?`, string(to), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("proposal %s not found", id)
	}
	return nil
}

func newProposalID(layer, kind string, payload []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(layer))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(kind))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(payload)
	return "prop_" + hex.EncodeToString(h.Sum(nil))[:10]
}
