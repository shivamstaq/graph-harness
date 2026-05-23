package code_core

import (
	"context"
	"database/sql"
)

// SelectorBinding is one row of the reverse selector→entity index. It
// records that a `selector_id` resolved to the calling entity at
// `bound_at_seq`, via the named anchor.
//
// Returned by Store.SelectorsBoundTo.
type SelectorBinding struct {
	SelectorID string
	FlowID     string
	ViaAnchor  string
	BoundAtSeq uint64
}

// initSelectorIndexSchema creates the entity_selector_index table and
// its supporting indexes if absent. Idempotent; safe to call on every
// store open. Mirrors the pattern in adjacency.go's initSchema.
//
// This complements semantic_overlay.Resolver's forward cache (keyed by
// selector-AST hash) — the reverse table is keyed by entity_id and
// returns the selectors bound to that entity, flipping Studio / TUI /
// IDE hover from a fan-out scan into a primary-key probe (P2.T35a /
// P2.M03).
func (s *Store) initSelectorIndexSchema() error {
	const schema = `
CREATE TABLE IF NOT EXISTS entity_selector_index (
    entity_id    TEXT NOT NULL,
    selector_id  TEXT NOT NULL,
    flow_id      TEXT,
    bound_at_seq INTEGER NOT NULL,
    via_anchor   TEXT NOT NULL,
    PRIMARY KEY (entity_id, selector_id)
);
CREATE INDEX IF NOT EXISTS idx_esi_selector ON entity_selector_index(selector_id);
`
	_, err := s.db.Exec(schema)
	return err
}

// BindSelector upserts a (entity_id, selector_id) row recording that
// `selectorID` resolved to `entityID` via `viaAnchor` at kernel seq
// `seq` inside the optional flow scope `flowID` (empty string = no
// flow). Idempotent at the primary key — re-binding refreshes
// bound_at_seq + via_anchor.
//
// Callers: semantic_overlay.Overlay.Resolve invokes this best-effort
// after each successful resolution (bound / reanchored outcomes).
func (s *Store) BindSelector(ctx context.Context, entityID, selectorID, flowID, viaAnchor string, seq uint64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO entity_selector_index (entity_id, selector_id, flow_id, bound_at_seq, via_anchor)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(entity_id, selector_id) DO UPDATE SET
		    flow_id = excluded.flow_id,
		    bound_at_seq = excluded.bound_at_seq,
		    via_anchor = excluded.via_anchor
	`, entityID, selectorID, nullableString(flowID), seq, viaAnchor)
	return err
}

// UnbindSelectorsForEntity drops every binding for the named entity.
// Called from the resolver on drift events that produce
// EntitySuperseded — the selectors must be re-resolved against the
// successor entity, so stale bindings would mislead hover surfaces.
func (s *Store) UnbindSelectorsForEntity(ctx context.Context, entityID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM entity_selector_index WHERE entity_id = ?`, entityID)
	return err
}

// UnbindSelector drops every binding for the named selector. Called
// when a selector is deleted from the overlay.
func (s *Store) UnbindSelector(ctx context.Context, selectorID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM entity_selector_index WHERE selector_id = ?`, selectorID)
	return err
}

// SelectorsBoundTo returns every selector currently bound to the named
// entity. O(1)-per-row via the primary-key prefix lookup on
// (entity_id, selector_id) — the lookup scans at most the rows for the
// given entity.
//
// Hover surfaces call this to answer "what selectors mention this
// entity?" in constant time. Empty slice (not nil error) when the
// entity has no bindings.
func (s *Store) SelectorsBoundTo(ctx context.Context, entityID string) ([]SelectorBinding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT selector_id, COALESCE(flow_id, ''), via_anchor, bound_at_seq
		FROM entity_selector_index
		WHERE entity_id = ?
		ORDER BY selector_id`, entityID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SelectorBinding
	for rows.Next() {
		var b SelectorBinding
		if err := rows.Scan(&b.SelectorID, &b.FlowID, &b.ViaAnchor, &b.BoundAtSeq); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// EntitiesForSelector returns every entity currently bound by the named
// selector. Uses the idx_esi_selector secondary index for symmetric O(1)
// lookup. Used by editor "find all references for selector S" flows.
func (s *Store) EntitiesForSelector(ctx context.Context, selectorID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT entity_id FROM entity_selector_index
		WHERE selector_id = ?
		ORDER BY entity_id`, selectorID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// nullableString returns sql.NullString{Valid:false} for "" so the
// flow_id column stores SQL NULL instead of the empty string. Keeps the
// "no flow scope" semantics distinct from a flow literally named "".
func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
