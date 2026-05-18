// Package facts defines the Facts Go interface (SPEC §6.4) — the universal,
// capability-gated storage adapter contract that every layer's backend
// implements — and the kernel's SQLite-backed event log.
package facts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Facts is the universal layer-storage adapter contract from SPEC §6.4.
// Methods are required only when the corresponding capability is declared
// in the layer's manifest; adapters return ErrCapabilityNotDeclared otherwise.
type Facts interface {
	Write(ctx context.Context, events []kernel.Event) (commitSeq uint64, err error)
	ReadCurrent(ctx context.Context, q kernel.LayerQuery) (kernel.ResultEnvelope, error)
	ReadAsOf(ctx context.Context, seq uint64, q kernel.LayerQuery) (kernel.ResultEnvelope, error)
	Snapshot(ctx context.Context, seq uint64) (kernel.SnapshotHandle, error)
	Restore(ctx context.Context, h kernel.SnapshotHandle) error
	Compact(ctx context.Context, beforeSeq uint64, h kernel.SnapshotHandle) error
	Subscribe(ctx context.Context, filter kernel.EventFilter) (EventStream, error)
}

// LegacySubscriberID is the implicit subscription_name passed by callers
// that have not been updated to the per-subscription cursor model from
// SPEC §6.16 (F8 / P0.5.T02). Defaulting to the empty string preserves
// pre-migration behavior where a subscriber had at most one persisted
// cursor per identity.
const LegacySubscriberID = ""

// EventStream is the subscription handle returned by Facts.Subscribe.
type EventStream interface {
	Events() <-chan kernel.Event
	Close() error
}

// ErrCapabilityNotDeclared is returned when a method is called on an adapter
// whose layer manifest did not declare the matching capability.
var ErrCapabilityNotDeclared = errors.New("capability not declared in layer manifest")

// EventLog is the kernel-owned event log: a single-writer SQLite store with
// global monotonic seqs, tx grouping, and per-subscriber cursor state.
// Implements a subset of Facts focused on append + subscribe; per-layer
// adapters wrap it for layer-local queries.
type EventLog struct {
	db *sql.DB

	mu         sync.Mutex // single-writer guarantee for Write
	lastSeq    uint64
	subsMu     sync.Mutex
	subs       map[uint64]*subscription
	subCounter uint64
}

// OpenEventLog opens (or creates) a SQLite event log at path. WAL mode is
// enabled per SPEC §6.16. Single-writer is enforced via the EventLog mutex.
func OpenEventLog(path string) (*EventLog, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := initEventLogSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	last, err := readMaxSeq(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &EventLog{
		db:      db,
		lastSeq: last,
		subs:    map[uint64]*subscription{},
	}, nil
}

// Close releases the underlying database handle.
func (e *EventLog) Close() error {
	e.subsMu.Lock()
	for _, s := range e.subs {
		_ = s.close()
	}
	e.subs = map[uint64]*subscription{}
	e.subsMu.Unlock()
	return e.db.Close()
}

// Append assigns sequential seqs to the events, persists them transactionally,
// and notifies subscribers. Single-writer per SPEC §6.16.
func (e *EventLog) Append(ctx context.Context, events []kernel.Event) (uint64, error) {
	if len(events) == 0 {
		return e.lastSeq, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO kernel_events (seq, ts, layer, kind, subject_layer, subject_kind, subject_id, payload, causes, produced_by, tx)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = stmt.Close() }()

	stamped := make([]kernel.Event, len(events))
	for i, ev := range events {
		ev.Seq = e.lastSeq + uint64(i) + 1
		if ev.TS.IsZero() {
			ev.TS = time.Now().UTC()
		}
		var sl, sk, si sql.NullString
		if ev.Subject != nil {
			sl.String, sl.Valid = ev.Subject.Layer, true
			sk.String, sk.Valid = ev.Subject.Kind, true
			si.String, si.Valid = ev.Subject.ID, true
		}
		causes, _ := encodeCauses(ev.Causes)
		if _, err := stmt.ExecContext(ctx,
			ev.Seq, ev.TS.UTC().Format(time.RFC3339Nano), ev.Layer, ev.Kind,
			sl, sk, si, []byte(ev.Payload), causes, string(ev.ProducedBy), ev.Tx); err != nil {
			return 0, err
		}
		stamped[i] = ev
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	e.lastSeq += uint64(len(events))

	// Fan out to subscribers (best-effort, never block producers).
	e.fanout(stamped)
	return e.lastSeq, nil
}

// LastSeq reports the most recently committed sequence number.
func (e *EventLog) LastSeq() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastSeq
}

// ReadAll returns every event in seq order. For tests and inspection only;
// production reads use bounded queries via Facts adapters.
func (e *EventLog) ReadAll(ctx context.Context) ([]kernel.Event, error) {
	rows, err := e.db.QueryContext(ctx, `
		SELECT seq, ts, layer, kind, subject_layer, subject_kind, subject_id,
		       payload, causes, produced_by, tx
		FROM kernel_events ORDER BY seq ASC
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanEvents(rows)
}

// ReadAsOf returns every event with seq <= maxSeq (inclusive) in seq order.
func (e *EventLog) ReadAsOf(ctx context.Context, maxSeq uint64) ([]kernel.Event, error) {
	rows, err := e.db.QueryContext(ctx, `
		SELECT seq, ts, layer, kind, subject_layer, subject_kind, subject_id,
		       payload, causes, produced_by, tx
		FROM kernel_events WHERE seq <= ? ORDER BY seq ASC
	`, maxSeq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanEvents(rows)
}

// CursorOf returns the last_processed_seq stored for (subID, subName)
// (zero if none). Per SPEC §6.16 + F8: cursors are scoped per
// subscription_name, so a single subscriber can multiplex many
// subscriptions across reconnects. Pass LegacySubscriberID ("") for
// the pre-migration single-cursor-per-subscriber behavior.
func (e *EventLog) CursorOf(ctx context.Context, subID, subName string) (uint64, error) {
	row := e.db.QueryRowContext(ctx,
		`SELECT last_processed_seq FROM layer_state WHERE subscriber_id = ? AND subscription_name = ?`,
		subID, subName)
	var v sql.NullInt64
	if err := row.Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	if !v.Valid {
		return 0, nil
	}
	return uint64(v.Int64), nil //nolint:gosec // monotonic, never negative
}

// AdvanceCursor stores the subscriber's last_processed_seq in the kernel-owned
// layer_state table — transactionally, per SPEC §6.16: do NOT emit events for
// cursor advancement (DESIGN §8). Per F8 / P0.5.T02 the cursor is keyed by
// (subscriber_id, subscription_name) so one subscriber can persist
// independent cursors across multiplexed subscriptions.
func (e *EventLog) AdvanceCursor(ctx context.Context, subID, subName string, seq uint64) error {
	_, err := e.db.ExecContext(ctx, `
		INSERT INTO layer_state (subscriber_id, subscription_name, last_processed_seq) VALUES (?, ?, ?)
		ON CONFLICT (subscriber_id, subscription_name) DO UPDATE SET last_processed_seq = excluded.last_processed_seq
	`, subID, subName, seq)
	return err
}

func initEventLogSchema(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS kernel_events (
    seq           INTEGER PRIMARY KEY,
    ts            TEXT    NOT NULL,
    layer         TEXT    NOT NULL,
    kind          TEXT    NOT NULL,
    subject_layer TEXT,
    subject_kind  TEXT,
    subject_id    TEXT,
    payload       BLOB    NOT NULL,
    causes        BLOB,
    produced_by   TEXT    NOT NULL,
    tx            TEXT
);
CREATE INDEX IF NOT EXISTS idx_events_layer_kind ON kernel_events(layer, kind);
CREATE INDEX IF NOT EXISTS idx_events_tx ON kernel_events(tx) WHERE tx IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_events_subject ON kernel_events(subject_layer, subject_kind, subject_id);

-- F8 / P0.5.T02: compound PK on (subscriber_id, subscription_name).
-- Pre-F8 deployments had PK on subscriber_id alone; migrateLayerState
-- below walks the rename dance for upgrades.
CREATE TABLE IF NOT EXISTS layer_state (
    subscriber_id      TEXT NOT NULL,
    subscription_name  TEXT NOT NULL DEFAULT '',
    last_processed_seq INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (subscriber_id, subscription_name)
);

CREATE TABLE IF NOT EXISTS layer_manifests (
    name        TEXT PRIMARY KEY,
    version     TEXT NOT NULL,
    yaml        BLOB NOT NULL,
    installed_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS kernel_snapshots (
    id    TEXT PRIMARY KEY,
    seq   INTEGER NOT NULL,
    layer TEXT NOT NULL,
    payload BLOB NOT NULL,
    created_at TEXT NOT NULL
);
`
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	return migrateLayerState(db)
}

// migrateLayerState upgrades pre-F8 layer_state rows (PK on subscriber_id
// alone) to the post-F8 compound PK shape. Idempotent: a fresh CREATE
// already has the new schema; an upgrade detects the legacy shape by
// looking for the subscription_name column and runs the SQLite-canonical
// rename dance.
func migrateLayerState(db *sql.DB) error {
	// Check whether subscription_name column already exists.
	rows, err := db.Query(`PRAGMA table_info(layer_state)`)
	if err != nil {
		return fmt.Errorf("table_info layer_state: %w", err)
	}
	hasSubName := false
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		if name == "subscription_name" {
			hasSubName = true
		}
	}
	_ = rows.Close()
	if hasSubName {
		return nil
	}
	// Rename dance: SQLite can't ALTER PRIMARY KEY in place.
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmts := []string{
		`ALTER TABLE layer_state RENAME TO layer_state_legacy`,
		`CREATE TABLE layer_state (
		    subscriber_id      TEXT NOT NULL,
		    subscription_name  TEXT NOT NULL DEFAULT '',
		    last_processed_seq INTEGER NOT NULL DEFAULT 0,
		    PRIMARY KEY (subscriber_id, subscription_name)
		)`,
		`INSERT INTO layer_state (subscriber_id, subscription_name, last_processed_seq)
		 SELECT subscriber_id, '', last_processed_seq FROM layer_state_legacy`,
		`DROP TABLE layer_state_legacy`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("migrate layer_state: %w (stmt=%q)", err, s)
		}
	}
	return tx.Commit()
}

func readMaxSeq(db *sql.DB) (uint64, error) {
	var v sql.NullInt64
	row := db.QueryRow(`SELECT MAX(seq) FROM kernel_events`)
	if err := row.Scan(&v); err != nil {
		return 0, err
	}
	if !v.Valid {
		return 0, nil
	}
	return uint64(v.Int64), nil //nolint:gosec // monotonic
}

func encodeCauses(c []uint64) ([]byte, error) {
	if len(c) == 0 {
		return nil, nil
	}
	// Compact comma-separated ASCII; readable in sqlite3 CLI.
	out := make([]byte, 0, 16)
	for i, v := range c {
		if i > 0 {
			out = append(out, ',')
		}
		out = appendUint(out, v)
	}
	return out, nil
}

func decodeCauses(b []byte) []uint64 {
	if len(b) == 0 {
		return nil
	}
	var out []uint64
	var cur uint64
	for _, ch := range b {
		if ch == ',' {
			out = append(out, cur)
			cur = 0
			continue
		}
		if ch < '0' || ch > '9' {
			continue
		}
		cur = cur*10 + uint64(ch-'0')
	}
	out = append(out, cur)
	return out
}

func appendUint(buf []byte, v uint64) []byte {
	if v == 0 {
		return append(buf, '0')
	}
	var tmp [20]byte
	i := len(tmp)
	for v > 0 {
		i--
		tmp[i] = byte('0' + v%10)
		v /= 10
	}
	return append(buf, tmp[i:]...)
}

func scanEvents(rows *sql.Rows) ([]kernel.Event, error) {
	var out []kernel.Event
	for rows.Next() {
		var (
			ev               kernel.Event
			tsStr            string
			sl, sk, si       sql.NullString
			payload, causes  []byte
			producedBy, txID sql.NullString
		)
		if err := rows.Scan(&ev.Seq, &tsStr, &ev.Layer, &ev.Kind,
			&sl, &sk, &si, &payload, &causes, &producedBy, &txID); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			return nil, fmt.Errorf("parse ts %q: %w", tsStr, err)
		}
		ev.TS = t
		if sl.Valid {
			ev.Subject = &kernel.EntityRef{Layer: sl.String, Kind: sk.String, ID: si.String}
		}
		ev.Payload = payload
		ev.Causes = decodeCauses(causes)
		if producedBy.Valid {
			ev.ProducedBy = kernel.SourceClass(producedBy.String)
		}
		if txID.Valid {
			ev.Tx = txID.String
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}
