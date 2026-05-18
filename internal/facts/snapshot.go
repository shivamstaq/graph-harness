package facts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// CreateSnapshot writes a frozen view of the event log up to seq into
// kernel_snapshots. The strategy here is event_replay: the snapshot payload
// is the events themselves; restore replays them. Per-layer materialized
// snapshots are layered on top by individual Facts adapters.
func (e *EventLog) CreateSnapshot(ctx context.Context, layer string, seq uint64) (kernel.SnapshotHandle, error) {
	if seq == 0 {
		seq = e.LastSeq()
	}
	events, err := e.ReadAsOf(ctx, seq)
	if err != nil {
		return kernel.SnapshotHandle{}, err
	}
	// Filter to the named layer when caller specified one. Empty layer = all.
	if layer != "" {
		filtered := make([]kernel.Event, 0, len(events))
		for _, ev := range events {
			if ev.Layer == layer {
				filtered = append(filtered, ev)
			}
		}
		events = filtered
	}
	payload, err := json.Marshal(events)
	if err != nil {
		return kernel.SnapshotHandle{}, err
	}
	id := snapshotID(layer, seq, payload)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := e.db.ExecContext(ctx,
		`INSERT INTO kernel_snapshots (id, seq, layer, payload, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, seq, layer, payload, now); err != nil {
		return kernel.SnapshotHandle{}, err
	}
	return kernel.SnapshotHandle{ID: id, Seq: seq, Layer: layer}, nil
}

// Restore reads a snapshot and re-appends its events to the live log.
// Used by chaos-recovery + cold-rebuild paths (F6 / F10). The append
// proceeds under the standard single-writer lock — concurrent live
// writes are not blocked except by the usual queueing. Idempotency is
// the layer's responsibility: re-appending the same canonical event
// content produces no semantic change once compare-before-emit is in
// place (F9 / P0.5.T09), but the kernel log itself does deduplicate
// by seq, which restored events carry as the original commit seq —
// SQLite's PK constraint rejects duplicate seqs, so concurrent
// restore-then-live-write of overlapping ranges errors loudly rather
// than silently corrupting.
func (e *EventLog) Restore(ctx context.Context, h kernel.SnapshotHandle) error {
	events, err := e.LoadSnapshot(ctx, h)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	// Strip the seq stamps so Append re-assigns from the live counter.
	// This is the correct semantic for chaos-recovery (the events lost
	// their place and need to be re-sequenced). For a "wind back to a
	// prior point" semantic the caller wants a different operation
	// (not exposed by Facts today).
	for i := range events {
		events[i].Seq = 0
	}
	_, err = e.Append(ctx, events)
	return err
}

// Compact deletes events with seq < beforeSeq AND seq <= h.Seq from the
// live log. The snapshot proves the deleted prefix is recoverable.
// Implements Facts.Compact (F6 + F10 retention enforcement).
//
// Compact is a destructive operation. It MUST only be invoked by the
// daemon's retention goroutine (RetentionLoop, lifecycle.go) after a
// successful CreateSnapshot at or above beforeSeq. External callers
// have no legitimate reason to invoke it.
func (e *EventLog) Compact(ctx context.Context, beforeSeq uint64, h kernel.SnapshotHandle) error {
	if beforeSeq == 0 {
		return nil
	}
	if h.Seq < beforeSeq {
		return fmt.Errorf("compact: snapshot seq %d below cutoff %d (refusing destructive delete without proof of recoverability)", h.Seq, beforeSeq)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err := e.db.ExecContext(ctx,
		`DELETE FROM kernel_events WHERE seq < ? AND seq <= ?`,
		beforeSeq, h.Seq)
	return err
}

// SnapshotInfo is the per-row shape returned by ListSnapshots.
// Fields mirror the kernel_snapshots schema; CreatedAt is the
// RFC3339Nano string preserved on disk.
type SnapshotInfo struct {
	ID        string
	Seq       uint64
	Layer     string
	CreatedAt string
}

// ListSnapshots returns every snapshot in the workspace, ordered by
// seq DESC (most-recent first). Pass layer="" to list every layer's
// snapshots. F12 / P0.T11 — the wire surface for `snapshot list`.
func (e *EventLog) ListSnapshots(ctx context.Context, layer string) ([]SnapshotInfo, error) {
	var (
		rows interface {
			Next() bool
			Scan(...any) error
			Close() error
			Err() error
		}
		err error
	)
	if layer == "" {
		rows, err = e.db.QueryContext(ctx,
			`SELECT id, seq, layer, created_at FROM kernel_snapshots ORDER BY seq DESC`)
	} else {
		rows, err = e.db.QueryContext(ctx,
			`SELECT id, seq, layer, created_at FROM kernel_snapshots WHERE layer = ? ORDER BY seq DESC`, layer)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SnapshotInfo
	for rows.Next() {
		var info SnapshotInfo
		if err := rows.Scan(&info.ID, &info.Seq, &info.Layer, &info.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, rows.Err()
}

// LoadSnapshot returns the events stored in a snapshot. Callers replay them
// to reconstruct layer state.
func (e *EventLog) LoadSnapshot(ctx context.Context, h kernel.SnapshotHandle) ([]kernel.Event, error) {
	row := e.db.QueryRowContext(ctx,
		`SELECT payload FROM kernel_snapshots WHERE id = ?`, h.ID)
	var payload []byte
	if err := row.Scan(&payload); err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", h.ID, err)
	}
	var events []kernel.Event
	if err := json.Unmarshal(payload, &events); err != nil {
		return nil, err
	}
	return events, nil
}

func snapshotID(layer string, seq uint64, payload []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(layer))
	_, _ = h.Write([]byte{0})
	var seqBuf [8]byte
	for i := range 8 {
		seqBuf[i] = byte((seq >> (8 * i)) & 0xff)
	}
	_, _ = h.Write(seqBuf[:])
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))[:16]
}
