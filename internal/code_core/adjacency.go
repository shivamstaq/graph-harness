package code_core

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Store is the SQLite-backed code.core adapter. It owns the entity table and
// the adjacency table that backs `calls` / `references` traversal (SPEC §6.14
// — adjacency in SQLite + planner BFS, no property-graph DB).
type Store struct {
	db *sql.DB
}

// NewStore wraps an opened *sql.DB and ensures the schema exists.
func NewStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) initSchema() error {
	const schema = `
CREATE TABLE IF NOT EXISTS code_entities (
    id             TEXT PRIMARY KEY,
    kind           TEXT NOT NULL,
    language_id    TEXT NOT NULL DEFAULT '',
    qualified_name TEXT,
    receiver       TEXT,
    path           TEXT,
    body_hash      TEXT,
    kind_tag       TEXT NOT NULL DEFAULT '',
    parent_id      TEXT NOT NULL DEFAULT '',
    ordinal        INTEGER NOT NULL DEFAULT 0,
    created_seq    INTEGER NOT NULL DEFAULT 0
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
`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
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
func (s *Store) PutEntity(ctx context.Context, e Entity, createdSeq uint64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO code_entities
		    (id, kind, language_id, qualified_name, receiver, path, body_hash,
		     kind_tag, parent_id, ordinal, created_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		    kind = excluded.kind,
		    language_id = excluded.language_id,
		    qualified_name = excluded.qualified_name,
		    receiver = excluded.receiver,
		    path = excluded.path,
		    body_hash = excluded.body_hash,
		    kind_tag = excluded.kind_tag,
		    parent_id = excluded.parent_id,
		    ordinal = excluded.ordinal
	`, e.ID, string(e.Kind), e.LanguageID, e.QualifiedName, e.Receiver,
		e.Path, e.BodyHash, e.KindTag, e.ParentID, e.Ordinal, createdSeq)
	return err
}

// UpsertProvenance records (or refreshes) a fact source's claim on an
// entity. Keyed by (entity_id, source_class) — one row per source.
// Subsequent calls for the same key update confidence / last_seen_seq /
// freshness so the table tracks the freshest observation per source.
func (s *Store) UpsertProvenance(ctx context.Context, entityID string, e SourceEntry) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO code_entity_provenance
		    (entity_id, source_class, confidence, last_seen_seq, freshness, produced_by)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(entity_id, source_class) DO UPDATE SET
		    confidence    = excluded.confidence,
		    last_seen_seq = excluded.last_seen_seq,
		    freshness     = excluded.freshness,
		    produced_by   = excluded.produced_by
	`, entityID, string(e.SourceClass), e.Confidence, e.LastSeenSeq, string(e.Freshness), e.ProducedBy)
	return err
}

// GetProvenance returns every recorded source claim for entityID, in
// stable ascending source_class order so callers can compare across
// runs without a separate sort step.
func (s *Store) GetProvenance(ctx context.Context, entityID string) ([]SourceEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT source_class, confidence, last_seen_seq, freshness, produced_by
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
			sc, freshness, producedBy string
			confidence                float64
			lastSeenSeq               uint64
		)
		if err := rows.Scan(&sc, &confidence, &lastSeenSeq, &freshness, &producedBy); err != nil {
			return nil, err
		}
		out = append(out, SourceEntry{
			SourceClass: SourceClass(sc),
			Confidence:  confidence,
			LastSeenSeq: lastSeenSeq,
			Freshness:   Freshness(freshness),
			ProducedBy:  producedBy,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// AddRelation inserts a typed edge (idempotent on dup).
func (s *Store) AddRelation(ctx context.Context, relation, fromID, toID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO code_relations (relation, from_id, to_id) VALUES (?, ?, ?)
	`, relation, fromID, toID)
	return err
}

// LookupByQualifiedNameSuffix returns the first entity whose qualified_name
// ends with `.<suffix>` or equals `<suffix>`. Used by change.process when
// resolving touched function names to fully-qualified code.core entities.
func (s *Store) LookupByQualifiedNameSuffix(ctx context.Context, suffix string) (*Entity, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, kind, language_id, qualified_name, receiver, path, body_hash
		 FROM code_entities
		 WHERE qualified_name = ? OR qualified_name LIKE '%.' || ?
		 LIMIT 1`, suffix, suffix)
	var e Entity
	var kind string
	var receiver, path, bodyHash sql.NullString
	if err := row.Scan(&e.ID, &kind, &e.LanguageID, &e.QualifiedName, &receiver, &path, &bodyHash); err != nil {
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
		`SELECT id, kind, language_id, qualified_name, receiver, path, body_hash
		 FROM code_entities WHERE qualified_name = ? LIMIT 1`, qn)
	var e Entity
	var kind string
	var receiver, path, bodyHash sql.NullString
	if err := row.Scan(&e.ID, &kind, &e.LanguageID, &e.QualifiedName, &receiver, &path, &bodyHash); err != nil {
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
