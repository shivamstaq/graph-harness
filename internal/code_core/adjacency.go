package code_core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Store is the SQLite-backed code.core adapter. It owns the entity table and
// the adjacency table that backs `calls` / `references` traversal (SPEC §6.14
// — adjacency in SQLite + planner BFS, no property-graph DB).
//
// Trust enforcement (P0.5.T18 + P1.5.T07 / SPEC §9.1): when a
// TrustPolicy is attached via SetTrustPolicy, every write seam
// (PutEntity, UpsertProvenance, AddRelation) consults it. In non-
// strict mode the policy is permissive — writes are stamped with
// the supplied createdSeq into kernel_seq_tag but no rejection
// happens. In strict mode (TrustPolicy.SetStrict(true)) writes
// without an explicit kernel.WriteToken require the Store to carry
// a token-issuer (installed by the daemon at boot); writes that
// reach the seam with no issuer + no explicit token + no whitelist
// fail with kernel.ErrMissingKernelSeqTag.
//
// The kernel_seq_tag column is additive across code_entities,
// code_entity_provenance, and code_relations — every row stamped
// at write time so AuditUntagged* can verify the writer-monopoly
// invariant.
type Store struct {
	db *sql.DB

	trustMu sync.RWMutex
	trust   *kernel.TrustPolicy
	issuer  TokenIssuer
}

// TokenIssuer mints a WriteToken at a given kernel seq. The daemon
// installs an issuer that consults its TrustPolicy.IssueWriteToken so
// in-process write seams (Unifier, Orchestrator, IngestParsedFile)
// can synthesize a valid token without threading the policy through
// every call signature. External callers without a policy installed
// hit the permissive legacy path.
type TokenIssuer interface {
	IssueWriteToken(seq uint64) kernel.WriteToken
}

// TokenIssuerFunc adapts a plain function to TokenIssuer.
type TokenIssuerFunc func(seq uint64) kernel.WriteToken

// IssueWriteToken satisfies TokenIssuer.
func (f TokenIssuerFunc) IssueWriteToken(seq uint64) kernel.WriteToken {
	return f(seq)
}

// NewStore wraps an opened *sql.DB and ensures the schema exists.
//
// initSchema runs CREATE-IF-NOT-EXISTS + ALTER TABLE additive
// migrations. The ALTERs fail on a read-only DB connection
// (`mode=ro` or `_pragma=query_only(1)`), so read-only callers
// must use NewStoreReadOnly, which skips migrations entirely and
// assumes the schema is already current.
func NewStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		return nil, err
	}
	return s, nil
}

// NewStoreReadOnly wraps a read-only *sql.DB. Skips the initSchema
// migrations because they would fail on a query_only connection.
// The caller is responsible for ensuring the schema is already
// current — typically by opening the same DB read-write at least
// once via NewStore before any read-only consumer runs.
//
// Used by batch-mode CLI (P0.5.T16 / SPEC §9.11) and daemon-routed
// read commands that open the daemon's SQLite store with `mode=ro`
// to avoid blocking the writer.
func NewStoreReadOnly(db *sql.DB) *Store {
	return &Store{db: db}
}

// SetTrustPolicy installs the kernel-issued trust policy on the
// store. Once installed, PutEntityWithToken (and future *WithToken
// methods) verify the caller's WriteToken before committing — non-
// daemon callers without a token receive ErrMissingKernelSeqTag.
//
// Passing nil disables verification (the default — preserves legacy
// behavior while the migration is in progress).
func (s *Store) SetTrustPolicy(p *kernel.TrustPolicy) {
	s.trustMu.Lock()
	s.trust = p
	s.trustMu.Unlock()
}

// SetTokenIssuer installs an in-process token issuer. The daemon
// installs `TokenIssuerFunc(policy.IssueWriteToken)` at boot so
// untokenized writes from in-daemon code paths auto-mint a daemon-
// authority token, while external callers (lacking the issuer)
// hit the strict-mode rejection seam.
func (s *Store) SetTokenIssuer(t TokenIssuer) {
	s.trustMu.Lock()
	s.issuer = t
	s.trustMu.Unlock()
}

// trustPolicy returns the installed policy (nil if none).
func (s *Store) trustPolicy() *kernel.TrustPolicy {
	s.trustMu.RLock()
	defer s.trustMu.RUnlock()
	return s.trust
}

// tokenIssuer returns the installed issuer (nil if none).
func (s *Store) tokenIssuer() TokenIssuer {
	s.trustMu.RLock()
	defer s.trustMu.RUnlock()
	return s.issuer
}

// authorizeImplicitWrite is the shared trust-policy check that
// PutEntity / UpsertProvenance / AddRelation invoke when called
// without an explicit WriteToken. In non-strict mode it always
// permits and returns the resolved seq tag (the caller's seq).
// In strict mode it requires an issuer to be installed; the
// issuer mints a token at the caller's seq which is then verified.
// Returns (seqTag, nil) on success, (0, ErrMissingKernelSeqTag)
// when strict-mode enforcement rejects the write.
func (s *Store) authorizeImplicitWrite(seq uint64) (uint64, error) {
	policy := s.trustPolicy()
	if policy == nil {
		// No policy installed — permissive (legacy / test path).
		return seq, nil
	}
	if !policy.IsStrict() {
		// Policy installed but not strict — stamp tag, no enforcement.
		return seq, nil
	}
	issuer := s.tokenIssuer()
	if issuer == nil {
		// Strict mode + no issuer = no way for in-process code to
		// authorize a write. Reject so the audit catches the gap.
		return 0, fmt.Errorf("code.core: strict trust mode requires a token issuer: %w", kernel.ErrMissingKernelSeqTag)
	}
	tok := issuer.IssueWriteToken(seq)
	if err := policy.Verify(tok, seq); err != nil {
		return 0, fmt.Errorf("code.core: write rejected: %w", err)
	}
	return tok.Seq(), nil
}

// PutEntityWithToken is the kernel-canonical write path. It verifies
// tok against the installed TrustPolicy, stamps the kernel_seq_tag
// column with tok.Seq(), and otherwise behaves like PutEntity.
//
// Returns kernel.ErrMissingKernelSeqTag (wrapped) when:
//   - a TrustPolicy is installed and tok is invalid or fails Verify, OR
//   - tok is the zero value (regardless of whether a policy is installed,
//     once strict-mode rolls out; today this only errors when a policy
//     is installed).
//
// Callers without legitimate authority (rogue CLI commands, library
// code paths, etc.) cannot construct a WriteToken — the type's
// fields are unexported, so the only way through this method is via
// kernel.TrustPolicy.IssueWriteToken / IssueImporterToken.
func (s *Store) PutEntityWithToken(ctx context.Context, e Entity, tok kernel.WriteToken, headSeq uint64) error {
	if policy := s.trustPolicy(); policy != nil {
		if err := policy.Verify(tok, headSeq); err != nil {
			return fmt.Errorf("code.core: write rejected: %w", err)
		}
	}
	// PutEntity already does the SQL upsert; we just need to stamp
	// the kernel_seq_tag column. Done as a single UPDATE after the
	// PutEntity completes so the tag is written transactionally
	// with the row.
	if err := s.PutEntity(ctx, e, tok.Seq()); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE code_entities SET kernel_seq_tag = ? WHERE id = ?`,
		tok.Seq(), e.ID); err != nil {
		return err
	}
	return nil
}

// ErrUntaggedWrite is the sentinel returned by audit helpers that
// find a row missing its kernel_seq_tag. Wraps kernel.ErrMissingKernelSeqTag
// so callers can use errors.Is across the seam.
var ErrUntaggedWrite = errors.Join(kernel.ErrMissingKernelSeqTag, errors.New("row missing kernel_seq_tag"))

// AuditUntaggedRows returns the IDs of any code_entities rows whose
// kernel_seq_tag is zero AND were created at a seq > minLegacySeq.
// Used by post-migration smoke tests to confirm that strict mode is
// safe to flip on — if the list is non-empty, some writer is still
// bypassing the tokenized path.
func (s *Store) AuditUntaggedRows(ctx context.Context, minLegacySeq uint64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM code_entities WHERE kernel_seq_tag = 0 AND created_seq > ?`,
		minLegacySeq)
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
	return out, rows.Err()
}

// AuditUntaggedProvenance returns (entity_id, source_class) pairs for
// every code_entity_provenance row whose kernel_seq_tag is zero AND
// last_seen_seq > minLegacySeq. Mirrors AuditUntaggedRows for the
// provenance table (P1.5.T07).
func (s *Store) AuditUntaggedProvenance(ctx context.Context, minLegacySeq uint64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT entity_id || '/' || source_class FROM code_entity_provenance
		 WHERE kernel_seq_tag = 0 AND last_seen_seq > ?`,
		minLegacySeq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// AuditUntaggedRelations returns (relation, from_id, to_id) tuples
// for every code_relations row whose kernel_seq_tag is zero. Relations
// do not carry a created_seq column today; the audit reports every
// legacy row so post-migration smoke tests catch any direct write.
func (s *Store) AuditUntaggedRelations(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT relation || '/' || from_id || '->' || to_id FROM code_relations
		 WHERE kernel_seq_tag = 0`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) initSchema() error {
	const schema = `
CREATE TABLE IF NOT EXISTS code_entities (
    id                   TEXT PRIMARY KEY,
    kind                 TEXT NOT NULL,
    language_id          TEXT NOT NULL DEFAULT '',
    qualified_name       TEXT,
    receiver             TEXT,
    path                 TEXT,
    body_hash            TEXT,
    kind_tag             TEXT NOT NULL DEFAULT '',
    parent_id            TEXT NOT NULL DEFAULT '',
    ordinal              INTEGER NOT NULL DEFAULT 0,
    normalized_signature TEXT NOT NULL DEFAULT '',
    symbol_fingerprint   TEXT NOT NULL DEFAULT '',
    ast_hash             TEXT NOT NULL DEFAULT '',
    created_seq          INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_code_entities_qn ON code_entities(qualified_name);
CREATE INDEX IF NOT EXISTS idx_code_entities_kind ON code_entities(kind);
CREATE INDEX IF NOT EXISTS idx_code_entities_parent ON code_entities(parent_id);

CREATE TABLE IF NOT EXISTS code_relations (
    relation TEXT NOT NULL,         -- "calls" | "references"
    from_id  TEXT NOT NULL,
    to_id    TEXT NOT NULL,
    PRIMARY KEY (relation, from_id, to_id)
);
CREATE INDEX IF NOT EXISTS idx_relations_from ON code_relations(relation, from_id);
CREATE INDEX IF NOT EXISTS idx_relations_to   ON code_relations(relation, to_id);

-- Per-entity provenance, keyed by (entity_id, source_class). One row per
-- fact source that has independently produced the entity. SPEC §6.11
-- (three-input model) + §4.4 (provenance fold). last_seen_seq feeds
-- freshness; confidence is the source's self-reported claim; freshness
-- is the per-source freshness class at the time of last observation.
CREATE TABLE IF NOT EXISTS code_entity_provenance (
    entity_id     TEXT NOT NULL,
    source_class  TEXT NOT NULL,
    confidence    REAL NOT NULL DEFAULT 1.0,
    last_seen_seq INTEGER NOT NULL DEFAULT 0,
    freshness     TEXT NOT NULL DEFAULT 'current',
    produced_by   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (entity_id, source_class),
    FOREIGN KEY (entity_id) REFERENCES code_entities(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_provenance_source ON code_entity_provenance(source_class);

-- SPEC §6.21 suppress-at-source: dedup index for SymbolDisambiguation
-- emissions. A disambiguation at (path, start_byte, end_byte) is
-- defined to be the *same* event whenever the same set of canonical
-- IDs claims that location. Re-running the orchestrator over an
-- unchanged workspace therefore produces zero new disambiguation
-- events because the prior claims_hash already lives in this table.
-- A genuinely new disagreement (different IDs, e.g. after a code
-- change) has a different claims_hash and DOES fire.
CREATE TABLE IF NOT EXISTS code_core_emitted_disambiguations (
    path        TEXT NOT NULL,
    start_byte  INTEGER NOT NULL,
    end_byte    INTEGER NOT NULL,
    claims_hash TEXT NOT NULL,
    emitted_seq INTEGER NOT NULL,
    PRIMARY KEY (path, start_byte, end_byte, claims_hash)
);
`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// Phase 1 anchor columns. Best-effort ALTER for stores opened before
	// the columns landed; SQLite errors on duplicates and we intentionally
	// swallow that — the column either exists already or was just added.
	_, _ = s.db.Exec(`ALTER TABLE code_entities ADD COLUMN normalized_signature TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE code_entities ADD COLUMN symbol_fingerprint TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE code_entities ADD COLUMN ast_hash TEXT NOT NULL DEFAULT ''`)
	// P1.L provenance columns. Same best-effort ALTER pattern.
	_, _ = s.db.Exec(`ALTER TABLE code_entity_provenance ADD COLUMN produced_by_path TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE code_entity_provenance ADD COLUMN server_id TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE code_entity_provenance ADD COLUMN low_confidence INTEGER NOT NULL DEFAULT 0`)
	// P0.5.T10: content_hash on File entities drives the cold-start
	// drift scan (SPEC §6.20). Populated for kind=File rows when a
	// file is ingested; the orchestrator's per-file fast-path skips
	// re-extract when the stored hash equals the current disk hash.
	_, _ = s.db.Exec(`ALTER TABLE code_entities ADD COLUMN content_hash TEXT NOT NULL DEFAULT ''`)
	// P0.5.T18 / SPEC §9.1: kernel_seq_tag stamps every write with
	// the kernel seq the daemon was at when it issued the write
	// token. Audit consumers query the column to attribute rows to
	// the kernel/importer/etc. The column is additive — pre-T18 rows
	// carry the zero value, which the row-level audit treats as
	// "legacy, source unverified."
	_, _ = s.db.Exec(`ALTER TABLE code_entities ADD COLUMN kernel_seq_tag INTEGER NOT NULL DEFAULT 0`)
	// P1.5.T07: extend the writer-monopoly tag to provenance and
	// relations so AuditUntagged* covers every code.core write seam,
	// not just code_entities. Additive ALTERs; pre-T07 rows carry
	// the zero value (audit treats as legacy).
	_, _ = s.db.Exec(`ALTER TABLE code_entity_provenance ADD COLUMN kernel_seq_tag INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE code_relations ADD COLUMN kernel_seq_tag INTEGER NOT NULL DEFAULT 0`)
	return s.migrateP0Provenance()
}

// migrateP0Provenance backfills a single tree-sitter provenance row for
// every code_entities row that has no provenance entry yet. P0 wrote
// entities without recording provenance; the unifier now expects every
// entity to have at least one source. Idempotent: rerunning is a no-op
// for already-migrated rows.
func (s *Store) migrateP0Provenance() error {
	const stmt = `
INSERT OR IGNORE INTO code_entity_provenance
    (entity_id, source_class, confidence, last_seen_seq, freshness, produced_by)
SELECT id, 'structural_treesitter', 1.0, created_seq, 'current', 'extractor:treesitter:p0'
FROM code_entities
WHERE id NOT IN (SELECT entity_id FROM code_entity_provenance)
`
	_, err := s.db.Exec(stmt)
	return err
}

// PutEntity inserts or replaces an entity. Idempotent at the same content
// (same id) — entity identity is content-addressable, so an upsert at the
// same id with different mutable fields (e.g. body_hash) reflects the
// latest observation rather than producing a new row.
//
// Trust enforcement (P0.5.T18 + P1.5.T07): when a TrustPolicy is
// installed, PutEntity routes through authorizeImplicitWrite. Strict
// mode requires a TokenIssuer to be attached to the Store (the daemon
// installs one at boot); without one the write fails with
// ErrMissingKernelSeqTag. The row's kernel_seq_tag column is stamped
// with the resolved seq so AuditUntaggedRows can verify the writer-
// monopoly invariant post-hoc.
func (s *Store) PutEntity(ctx context.Context, e Entity, createdSeq uint64) error {
	tag, err := s.authorizeImplicitWrite(createdSeq)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO code_entities
		    (id, kind, language_id, qualified_name, receiver, path, body_hash,
		     kind_tag, parent_id, ordinal,
		     normalized_signature, symbol_fingerprint, ast_hash, created_seq,
		     kernel_seq_tag)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		    kind = excluded.kind,
		    language_id = excluded.language_id,
		    qualified_name = excluded.qualified_name,
		    receiver = excluded.receiver,
		    path = excluded.path,
		    body_hash = excluded.body_hash,
		    kind_tag = excluded.kind_tag,
		    parent_id = excluded.parent_id,
		    ordinal = excluded.ordinal,
		    normalized_signature = excluded.normalized_signature,
		    symbol_fingerprint = excluded.symbol_fingerprint,
		    ast_hash = excluded.ast_hash,
		    kernel_seq_tag = excluded.kernel_seq_tag
	`, e.ID, string(e.Kind), e.LanguageID, e.QualifiedName, e.Receiver,
		e.Path, e.BodyHash, e.KindTag, e.ParentID, e.Ordinal,
		e.NormalizedSignature, e.SymbolFingerprint, e.ASTHash, createdSeq, tag)
	return err
}

// UpsertProvenance records (or refreshes) a fact source's claim on an
// entity. Keyed by (entity_id, source_class) — one row per source.
// Subsequent calls for the same key update confidence / last_seen_seq /
// freshness so the table tracks the freshest observation per source.
//
// Trust enforcement (P1.5.T07): same authorizeImplicitWrite path as
// PutEntity. The kernel_seq_tag column on code_entity_provenance is
// stamped with the resolved seq.
func (s *Store) UpsertProvenance(ctx context.Context, entityID string, e SourceEntry) error {
	tag, err := s.authorizeImplicitWrite(e.LastSeenSeq)
	if err != nil {
		return err
	}
	low := 0
	if e.LowConfidence {
		low = 1
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO code_entity_provenance
		    (entity_id, source_class, confidence, last_seen_seq, freshness,
		     produced_by, produced_by_path, server_id, low_confidence, kernel_seq_tag)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(entity_id, source_class) DO UPDATE SET
		    confidence       = excluded.confidence,
		    last_seen_seq    = excluded.last_seen_seq,
		    freshness        = excluded.freshness,
		    produced_by      = excluded.produced_by,
		    produced_by_path = excluded.produced_by_path,
		    server_id        = excluded.server_id,
		    low_confidence   = excluded.low_confidence,
		    kernel_seq_tag   = excluded.kernel_seq_tag
	`, entityID, string(e.SourceClass), e.Confidence, e.LastSeenSeq, string(e.Freshness),
		e.ProducedBy, e.ProducedByPath, e.ServerID, low, tag)
	return err
}

// GetProvenance returns every recorded source claim for entityID, in
// stable ascending source_class order so callers can compare across
// runs without a separate sort step.
func (s *Store) GetProvenance(ctx context.Context, entityID string) ([]SourceEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT source_class, confidence, last_seen_seq, freshness,
		       produced_by, produced_by_path, server_id, low_confidence
		FROM code_entity_provenance
		WHERE entity_id = ?
		ORDER BY source_class ASC
	`, entityID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SourceEntry
	for rows.Next() {
		var (
			sc, freshness, producedBy, producedByPath, serverID string
			confidence                                          float64
			lastSeenSeq                                         uint64
			low                                                 int
		)
		if err := rows.Scan(&sc, &confidence, &lastSeenSeq, &freshness,
			&producedBy, &producedByPath, &serverID, &low); err != nil {
			return nil, err
		}
		out = append(out, SourceEntry{
			SourceClass:    SourceClass(sc),
			Confidence:     confidence,
			LastSeenSeq:    lastSeenSeq,
			Freshness:      Freshness(freshness),
			ProducedBy:     producedBy,
			ProducedByPath: producedByPath,
			ServerID:       serverID,
			LowConfidence:  low != 0,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// AddRelation inserts a typed edge (idempotent on dup). The createdSeq
// stamps the kernel_seq_tag column for trust enforcement (P1.5.T07);
// callers in non-strict mode can pass 0 if they have no seq context
// — the audit treats kernel_seq_tag=0 as "legacy, source unverified."
func (s *Store) AddRelation(ctx context.Context, relation, fromID, toID string, createdSeq uint64) error {
	tag, err := s.authorizeImplicitWrite(createdSeq)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO code_relations (relation, from_id, to_id, kernel_seq_tag) VALUES (?, ?, ?, ?)
	`, relation, fromID, toID, tag)
	return err
}

// LookupByQualifiedNameSuffix returns the first entity whose qualified_name
// ends with `.<suffix>` or equals `<suffix>`. Used by change.process when
// resolving touched function names to fully-qualified code.core entities.
func (s *Store) LookupByQualifiedNameSuffix(ctx context.Context, suffix string) (*Entity, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, kind, language_id, qualified_name, receiver, path, body_hash,
		        kind_tag, parent_id, ordinal,
		        normalized_signature, symbol_fingerprint, ast_hash
		 FROM code_entities
		 WHERE qualified_name = ? OR qualified_name LIKE '%.' || ?
		 LIMIT 1`, suffix, suffix)
	var e Entity
	var kind string
	var receiver, path, bodyHash sql.NullString
	if err := row.Scan(&e.ID, &kind, &e.LanguageID, &e.QualifiedName, &receiver, &path, &bodyHash,
		&e.KindTag, &e.ParentID, &e.Ordinal,
		&e.NormalizedSignature, &e.SymbolFingerprint, &e.ASTHash); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	e.Kind = EntityKind(kind)
	e.Receiver = receiver.String
	e.Path = path.String
	e.BodyHash = bodyHash.String
	return &e, nil
}

// LookupEntityByID fetches an entity by its content-addressable ID.
// Returns (nil, nil) when no entity with id exists. The query API in
// [provenance_query.go] uses this; surface adapters receiving an ID
// via selector binding can use it directly.
func (s *Store) LookupEntityByID(ctx context.Context, id string) (*Entity, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, kind, language_id, qualified_name, receiver, path, body_hash,
		        kind_tag, parent_id, ordinal,
		        normalized_signature, symbol_fingerprint, ast_hash
		 FROM code_entities WHERE id = ? LIMIT 1`, id)
	var e Entity
	var kind string
	var receiver, path, bodyHash sql.NullString
	if err := row.Scan(&e.ID, &kind, &e.LanguageID, &e.QualifiedName, &receiver, &path, &bodyHash,
		&e.KindTag, &e.ParentID, &e.Ordinal,
		&e.NormalizedSignature, &e.SymbolFingerprint, &e.ASTHash); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	e.Kind = EntityKind(kind)
	e.Receiver = receiver.String
	e.Path = path.String
	e.BodyHash = bodyHash.String
	return &e, nil
}

// LookupByQualifiedName returns the first entity matching qn (case-sensitive).
// Used by selector resolution.
func (s *Store) LookupByQualifiedName(ctx context.Context, qn string) (*Entity, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, kind, language_id, qualified_name, receiver, path, body_hash,
		        kind_tag, parent_id, ordinal,
		        normalized_signature, symbol_fingerprint, ast_hash
		 FROM code_entities WHERE qualified_name = ? LIMIT 1`, qn)
	var e Entity
	var kind string
	var receiver, path, bodyHash sql.NullString
	if err := row.Scan(&e.ID, &kind, &e.LanguageID, &e.QualifiedName, &receiver, &path, &bodyHash,
		&e.KindTag, &e.ParentID, &e.Ordinal,
		&e.NormalizedSignature, &e.SymbolFingerprint, &e.ASTHash); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	e.Kind = EntityKind(kind)
	e.Receiver = receiver.String
	e.Path = path.String
	e.BodyHash = bodyHash.String
	return &e, nil
}

// LookupAllByQualifiedName returns every entity whose qualified_name
// equals qn. Distinct from LookupByQualifiedName, which returns only the
// first match — selector resolution wants the full candidate set so a
// `unique` selector can raise cardinality violations and a `language_id`
// filter can narrow polyglot matches to one language.
func (s *Store) LookupAllByQualifiedName(ctx context.Context, qn string) ([]Entity, error) {
	if qn == "" {
		return nil, nil
	}
	return s.queryEntities(ctx,
		`SELECT id, kind, language_id, qualified_name, receiver, path, body_hash,
		        kind_tag, parent_id, ordinal,
		        normalized_signature, symbol_fingerprint, ast_hash
		 FROM code_entities WHERE qualified_name = ?`, qn)
}

// LookupByBodyHash returns every entity whose body_hash equals bh. Used by
// the body_hash anchor evaluator (SPEC §3.1). Empty bh returns no rows
// (the empty body hash is treated as "no signal", not a wildcard).
func (s *Store) LookupByBodyHash(ctx context.Context, bh string) ([]Entity, error) {
	if bh == "" {
		return nil, nil
	}
	return s.queryEntities(ctx,
		`SELECT id, kind, language_id, qualified_name, receiver, path, body_hash,
		        kind_tag, parent_id, ordinal,
		        normalized_signature, symbol_fingerprint, ast_hash
		 FROM code_entities WHERE body_hash = ?`, bh)
}

// LookupBySymbolFingerprint returns every entity matching the supplied
// fingerprint. Empty fp returns no rows.
func (s *Store) LookupBySymbolFingerprint(ctx context.Context, fp string) ([]Entity, error) {
	if fp == "" {
		return nil, nil
	}
	return s.queryEntities(ctx,
		`SELECT id, kind, language_id, qualified_name, receiver, path, body_hash,
		        kind_tag, parent_id, ordinal,
		        normalized_signature, symbol_fingerprint, ast_hash
		 FROM code_entities WHERE symbol_fingerprint = ?`, fp)
}

// LookupByASTHash returns every entity matching the supplied AST hash.
// Empty hash returns no rows.
func (s *Store) LookupByASTHash(ctx context.Context, h string) ([]Entity, error) {
	if h == "" {
		return nil, nil
	}
	return s.queryEntities(ctx,
		`SELECT id, kind, language_id, qualified_name, receiver, path, body_hash,
		        kind_tag, parent_id, ordinal,
		        normalized_signature, symbol_fingerprint, ast_hash
		 FROM code_entities WHERE ast_hash = ?`, h)
}

// LookupFunctions returns every Function/Method entity. Used by the
// function_signature anchor evaluator and the path_glob evaluator as the
// candidate set. Avoid in hot paths on large stores; the signature
// matcher iterates the full result here (P0 indexer scale; P3 will
// introduce a normalized-signature index).
func (s *Store) LookupFunctions(ctx context.Context) ([]Entity, error) {
	return s.queryEntities(ctx,
		`SELECT id, kind, language_id, qualified_name, receiver, path, body_hash,
		        kind_tag, parent_id, ordinal,
		        normalized_signature, symbol_fingerprint, ast_hash
		 FROM code_entities WHERE kind IN ('Function','Method')`)
}

// LookupAllEntities returns every entity. Used as the candidate set for
// path_glob (which is kind-agnostic). Same hot-path caveat applies.
func (s *Store) LookupAllEntities(ctx context.Context) ([]Entity, error) {
	return s.queryEntities(ctx,
		`SELECT id, kind, language_id, qualified_name, receiver, path, body_hash,
		        kind_tag, parent_id, ordinal,
		        normalized_signature, symbol_fingerprint, ast_hash
		 FROM code_entities`)
}

// CallerNames returns the qualified_name of every entity that calls toID.
// Used by the call_neighborhood anchor evaluator.
func (s *Store) CallerNames(ctx context.Context, toID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.qualified_name FROM code_entities e
		 JOIN code_relations r ON r.from_id = e.id
		 WHERE r.relation = 'calls' AND r.to_id = ? AND e.qualified_name IS NOT NULL`, toID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanStrings(rows)
}

// CalleeNames returns the qualified_name of every entity called by fromID.
func (s *Store) CalleeNames(ctx context.Context, fromID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.qualified_name FROM code_entities e
		 JOIN code_relations r ON r.to_id = e.id
		 WHERE r.relation = 'calls' AND r.from_id = ? AND e.qualified_name IS NOT NULL`, fromID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanStrings(rows)
}

// queryEntities is the shared row-scanning helper for the multi-row
// LookupBy* queries above. It assumes the SELECT projects the same column
// list scanned into Entity.
func (s *Store) queryEntities(ctx context.Context, query string, args ...any) ([]Entity, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Entity
	for rows.Next() {
		var e Entity
		var kind string
		var receiver, path, bodyHash sql.NullString
		if err := rows.Scan(&e.ID, &kind, &e.LanguageID, &e.QualifiedName,
			&receiver, &path, &bodyHash, &e.KindTag, &e.ParentID, &e.Ordinal,
			&e.NormalizedSignature, &e.SymbolFingerprint, &e.ASTHash); err != nil {
			return nil, err
		}
		e.Kind = EntityKind(kind)
		e.Receiver = receiver.String
		e.Path = path.String
		e.BodyHash = bodyHash.String
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanStrings(rows *sql.Rows) ([]string, error) {
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteEntity removes an entity by ID. Used by the change.process tests
// and by future P3 SymbolDeleted event handlers; production deletes flow
// through the unifier so this method is intentionally narrow (no cascade
// beyond the FK-driven cleanup of code_entity_provenance).
func (s *Store) DeleteEntity(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM code_entities WHERE id = ?`, id)
	return err
}

// ListFilter narrows [Store.ListEntities] results. All fields are
// optional; the empty filter returns every entity. Filters AND
// together — populating both fields requires an exact match on both
// columns.
type ListFilter struct {
	QualifiedName string
	LanguageID    string
}

// ListEntitiesByPath returns every entity whose `path` column matches
// the supplied workspace-relative path. Used by the cold-start drift
// scan + watcher remove-handler to enumerate the children of a
// removed File so the SymbolDeleted cascade can fire one event per
// child (F7 / SPEC §6.20 hydration model). The File entity itself
// is included; callers filter it out by `kind == KindFile` if they
// want only the descendants.
func (s *Store) ListEntitiesByPath(ctx context.Context, path string) ([]Entity, error) {
	if path == "" {
		return nil, nil
	}
	return s.queryEntities(ctx,
		`SELECT id, kind, language_id, qualified_name, receiver, path, body_hash,
		        kind_tag, parent_id, ordinal,
		        normalized_signature, symbol_fingerprint, ast_hash
		 FROM code_entities WHERE path = ? ORDER BY id ASC`, path)
}

// ListEntities returns every entity matching the filter, ordered by
// (language_id, qualified_name, id) so two equal queries return rows
// in the same order across runs. The empty filter walks the whole
// table; callers that only care about counts should prefer
// [Store.CountByKind] or a SQL count to avoid materializing rows.
// Used by the `graph-harness code list` CLI to support multi-entity
// e2e assertions ("Go and Python both materialize User.save").
func (s *Store) ListEntities(ctx context.Context, f ListFilter) ([]Entity, error) {
	const baseQuery = `SELECT id, kind, language_id, qualified_name, receiver, path, body_hash,
		        kind_tag, parent_id, ordinal,
		        normalized_signature, symbol_fingerprint, ast_hash
		 FROM code_entities`
	var (
		conds []string
		args  []any
	)
	if f.QualifiedName != "" {
		conds = append(conds, "qualified_name = ?")
		args = append(args, f.QualifiedName)
	}
	if f.LanguageID != "" {
		conds = append(conds, "language_id = ?")
		args = append(args, f.LanguageID)
	}
	q := baseQuery
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY language_id ASC, qualified_name ASC, id ASC"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Entity
	for rows.Next() {
		var e Entity
		var kind string
		var receiver, path, bodyHash sql.NullString
		if err := rows.Scan(&e.ID, &kind, &e.LanguageID, &e.QualifiedName, &receiver, &path, &bodyHash,
			&e.KindTag, &e.ParentID, &e.Ordinal,
			&e.NormalizedSignature, &e.SymbolFingerprint, &e.ASTHash); err != nil {
			return nil, err
		}
		e.Kind = EntityKind(kind)
		e.Receiver = receiver.String
		e.Path = path.String
		e.BodyHash = bodyHash.String
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// CountByKind returns how many entities of a given kind are stored.
func (s *Store) CountByKind(ctx context.Context, kind EntityKind) (int, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM code_entities WHERE kind = ?`, string(kind))
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// BFS performs a bounded breadth-first traversal of the named relation,
// starting at startID, up to maxDepth hops. Used by change.process stage 5
// to compute the impacted set (P0.T31). Naive in P0; Mangle-driven in P3.
func (s *Store) BFS(ctx context.Context, relation, startID string, maxDepth int) ([]string, error) {
	visited := map[string]struct{}{startID: {}}
	frontier := []string{startID}
	for d := 0; d < maxDepth && len(frontier) > 0; d++ {
		args := make([]any, 0, len(frontier)+1)
		args = append(args, relation)
		placeholders := make([]string, len(frontier))
		for i, id := range frontier {
			placeholders[i] = "?"
			args = append(args, id)
		}
		query := fmt.Sprintf(
			`SELECT to_id FROM code_relations WHERE relation = ? AND from_id IN (%s)`,
			joinComma(placeholders))
		// #nosec G201,G202 -- placeholders are constant "?", IDs are bound
		// via args. The relation parameter is also bound. No string concat
		// of user data into SQL.
		next, err := s.bfsHopOnce(ctx, query, args)
		if err != nil {
			return nil, err
		}
		fresh := next[:0]
		for _, to := range next {
			if _, seen := visited[to]; !seen {
				visited[to] = struct{}{}
				fresh = append(fresh, to)
			}
		}
		frontier = fresh
	}
	delete(visited, startID)
	out := make([]string, 0, len(visited))
	for id := range visited {
		out = append(out, id)
	}
	return out, nil
}

func joinComma(s []string) string {
	return strings.Join(s, ",")
}

func (s *Store) bfsHopOnce(ctx context.Context, query string, args []any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var to string
		if err := rows.Scan(&to); err != nil {
			return nil, err
		}
		out = append(out, to)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
