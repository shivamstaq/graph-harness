package code_core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"math"
	"sort"
	"strings"
)

// ContentFingerprint returns a deterministic fingerprint over the
// semantic content of an Entity. Two entities with the same
// fingerprint represent the same observable state at the code.core
// layer; re-emitting an event for an entity whose fingerprint is
// unchanged is forbidden by SPEC §6.21 (suppress-at-source).
//
// The fingerprint deliberately excludes any seq-derived field
// (created_seq, last_seen_seq) since those track *when* the entity
// was observed, not *what* the entity is. They are the freshness /
// observation axis; the fingerprint is the state axis.
func (e Entity) ContentFingerprint() string {
	h := sha256.New()
	for _, s := range []string{
		string(e.Kind), e.LanguageID, e.QualifiedName, e.Receiver,
		e.Path, e.BodyHash, e.KindTag, e.ParentID,
		e.NormalizedSignature, e.SymbolFingerprint, e.ASTHash,
	} {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	var ord [4]byte
	for i := range 4 {
		ord[i] = byte((e.Ordinal >> (8 * i)) & 0xff)
	}
	_, _ = h.Write(ord[:])
	return hex.EncodeToString(h.Sum(nil))
}

// ContentFingerprint returns the fingerprint of a SourceEntry's
// non-seq content. last_seen_seq is excluded so that observation
// refreshes (same source re-observing the same entity at a higher
// seq) are not classified as state transitions.
func (s SourceEntry) ContentFingerprint() string {
	h := sha256.New()
	for _, v := range []string{
		string(s.SourceClass), string(s.Freshness),
		s.ProducedBy, s.ProducedByPath, s.ServerID,
	} {
		_, _ = h.Write([]byte(v))
		_, _ = h.Write([]byte{0})
	}
	var conf [8]byte
	bits := math.Float64bits(s.Confidence)
	for i := range 8 {
		conf[i] = byte((bits >> (8 * i)) & 0xff)
	}
	_, _ = h.Write(conf[:])
	if s.LowConfidence {
		_, _ = h.Write([]byte{1})
	} else {
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// EntityFingerprintAt fetches the content fingerprint of the entity
// stored at id, returning ("", false, nil) when the entity does not
// exist. The fingerprint is computed over the same fields as
// Entity.ContentFingerprint so a callers can compare a proposed
// write's fingerprint against the stored state without reading the
// full row twice.
func (s *Store) EntityFingerprintAt(ctx context.Context, id string) (string, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT kind, language_id, qualified_name, receiver, path, body_hash,
		       kind_tag, parent_id, ordinal,
		       normalized_signature, symbol_fingerprint, ast_hash
		FROM code_entities WHERE id = ? LIMIT 1
	`, id)
	var e Entity
	var kind string
	var receiver, path, bodyHash sql.NullString
	if err := row.Scan(&kind, &e.LanguageID, &e.QualifiedName, &receiver, &path, &bodyHash,
		&e.KindTag, &e.ParentID, &e.Ordinal,
		&e.NormalizedSignature, &e.SymbolFingerprint, &e.ASTHash); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	e.Kind = EntityKind(kind)
	e.Receiver = receiver.String
	e.Path = path.String
	e.BodyHash = bodyHash.String
	return e.ContentFingerprint(), true, nil
}

// ProvenanceFingerprintAt fetches the content fingerprint of the
// provenance row at (entityID, sourceClass), returning ("", false,
// nil) when no row exists. Mirrors EntityFingerprintAt and applies
// the same seq-excluded definition (SourceEntry.ContentFingerprint).
func (s *Store) ProvenanceFingerprintAt(ctx context.Context, entityID string, sc SourceClass) (string, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT source_class, confidence, last_seen_seq, freshness,
		       produced_by, produced_by_path, server_id, low_confidence
		FROM code_entity_provenance
		WHERE entity_id = ? AND source_class = ? LIMIT 1
	`, entityID, string(sc))
	var (
		gotSC, freshness, producedBy, producedByPath, serverID string
		confidence                                              float64
		lastSeenSeq                                             uint64
		low                                                     int
	)
	if err := row.Scan(&gotSC, &confidence, &lastSeenSeq, &freshness,
		&producedBy, &producedByPath, &serverID, &low); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	e := SourceEntry{
		SourceClass:    SourceClass(gotSC),
		Confidence:     confidence,
		LastSeenSeq:    lastSeenSeq,
		Freshness:      Freshness(freshness),
		ProducedBy:     producedBy,
		ProducedByPath: producedByPath,
		ServerID:       serverID,
		LowConfidence:  low != 0,
	}
	return e.ContentFingerprint(), true, nil
}

// PutEntityIfChanged inserts or updates the entity at e.ID and
// reports whether the stored content changed as a result. Callers
// observing changed=false MUST NOT emit a state-transition event
// for the entity per SPEC §6.21 (compare-before-emit).
//
// The implementation refreshes the row unconditionally (so
// created_seq picks up the latest observation seq even when content
// is unchanged) but returns changed=false when the fingerprint of
// the proposed Entity equals the fingerprint of the stored Entity.
func (s *Store) PutEntityIfChanged(ctx context.Context, e Entity, createdSeq uint64) (bool, error) {
	prev, present, err := s.EntityFingerprintAt(ctx, e.ID)
	if err != nil {
		return false, err
	}
	next := e.ContentFingerprint()
	if present && prev == next {
		// Pure observation refresh — do not rewrite the row. The
		// last_seen_seq refresh happens out-of-band via the per-source
		// UpsertProvenance call so the SourceEntry row reflects the
		// latest observation seq without disturbing the entity row.
		return false, nil
	}
	return true, s.PutEntity(ctx, e, createdSeq)
}

// UpsertProvenanceIfChanged writes or refreshes the provenance row
// and reports whether the stored content changed. last_seen_seq is
// excluded from the fingerprint per ContentFingerprint, so a re-
// observation by the same source at a higher seq is not classified
// as a state transition — it still refreshes the row (so freshness
// is observable) but returns changed=false to the caller.
func (s *Store) UpsertProvenanceIfChanged(ctx context.Context, entityID string, sp SourceEntry) (bool, error) {
	prev, present, err := s.ProvenanceFingerprintAt(ctx, entityID, sp.SourceClass)
	if err != nil {
		return false, err
	}
	next := sp.ContentFingerprint()
	changed := !present || prev != next
	// Always refresh the row (last_seen_seq + confidence may have
	// moved); changed reports the semantic-transition axis only.
	if err := s.UpsertProvenance(ctx, entityID, sp); err != nil {
		return false, err
	}
	return changed, nil
}

// MarkDisambiguationEmitted records that a disambiguation event with
// claimsHash has been emitted at (path, startByte, endByte). Returns
// (true, nil) if this is a new emission (caller should fire the
// event); returns (false, nil) if an event with the same claims was
// already emitted at this location (caller MUST NOT re-emit per
// SPEC §6.21). The table is keyed by the claims fingerprint so a
// genuinely different disagreement at the same location DOES fire a
// new event.
func (s *Store) MarkDisambiguationEmitted(ctx context.Context, path string, startByte, endByte uint32, claimsHash string, emittedSeq uint64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO code_core_emitted_disambiguations
		    (path, start_byte, end_byte, claims_hash, emitted_seq)
		VALUES (?, ?, ?, ?, ?)
	`, path, startByte, endByte, claimsHash, emittedSeq)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// DisambiguationClaimsHash returns the fingerprint of a sorted list
// of canonical IDs. The fingerprint is deterministic: re-running the
// unifier with the same set of claimants at the same location yields
// the same hash, so MarkDisambiguationEmitted suppresses the
// re-emission.
func DisambiguationClaimsHash(canonicalIDs []string) string {
	ids := append([]string(nil), canonicalIDs...)
	sort.Strings(ids)
	joined := strings.Join(ids, "\x00")
	h := sha256.Sum256([]byte(joined))
	return hex.EncodeToString(h[:])
}

