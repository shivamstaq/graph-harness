package jsonrpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sourcegraph/jsonrpc2"
)

// IdentifyParams is the param shape for kernel.identify.
type IdentifyParams struct {
	SubscriberID string `json:"subscriber_id"`
}

// IdentifyResult echoes the bound subscriber_id back to the client.
type IdentifyResult struct {
	SubscriberID string `json:"subscriber_id"`
}

// Identify implements kernel.identify (SPEC §6.22 connect step).
func (s *Service) Identify(ctx context.Context, conn *jsonrpc2.Conn, p IdentifyParams) (IdentifyResult, error) {
	s.subscriptions().Identify(conn, p.SubscriberID)
	return IdentifyResult{SubscriberID: s.subscriptions().SubscriberIDFor(conn)}, nil
}

// SubscribeParams is the param shape for kernel.subscribe.
type SubscribeParams struct {
	// Filter is a layer or layer/kind shorthand selecting which
	// events fan out to this subscription. Empty matches everything.
	Filter string `json:"filter"`
	// Cursor is the last seq the client already processed. When set,
	// the daemon resumes from cursor+1; when zero, the stream begins
	// at the current head (or at the persisted cursor for this
	// (subscriber_id, name) pair if present — see SPEC §6.22 + F8).
	Cursor uint64 `json:"cursor,omitempty"`
	// QueueCap overrides the default per-subscription backpressure
	// window. Backpressure here is measured as (cursor - ackedSeq):
	// when the gap exceeds QueueCap, the daemon emits one
	// `kernel.fellBehind` notification carrying the client's
	// recovery options. Zero/omitted uses the default (SPEC §6.22).
	QueueCap int `json:"queue_cap,omitempty"`
	// Name is the subscription identifier persisted alongside
	// subscriber_id for cross-restart cursor lookup (F8 / P0.5.T02).
	// Two subscriptions from the same subscriber with different Name
	// values get independent persisted cursors. Empty defaults to
	// the filter expression, so the common case "one subscriber,
	// one subscription per filter" works without explicit naming.
	Name string `json:"name,omitempty"`
}

// SubscribeResult carries the subscription_id back to the client.
type SubscribeResult struct {
	SubscriptionID string `json:"subscription_id"`
	SubscriberID   string `json:"subscriber_id"`
}

// Subscribe implements kernel.subscribe (SPEC §6.22 subscribe step).
func (s *Service) Subscribe(ctx context.Context, conn *jsonrpc2.Conn, p SubscribeParams) (SubscribeResult, error) {
	id, err := s.subscriptions().SubscribeWithName(ctx, conn, p.Filter, p.Cursor, SubscribeOptions{
		Name:     p.Name,
		QueueCap: p.QueueCap,
	})
	if err != nil {
		return SubscribeResult{}, err
	}
	return SubscribeResult{
		SubscriptionID: id,
		SubscriberID:   s.subscriptions().SubscriberIDFor(conn),
	}, nil
}

// AckParams is the param shape for kernel.ack.
type AckParams struct {
	SubscriptionID string `json:"subscription_id"`
	Seq            uint64 `json:"seq"`
}

// Ack implements kernel.ack (SPEC §6.22 ack step).
func (s *Service) Ack(ctx context.Context, p AckParams) (struct{}, error) {
	if err := s.subscriptions().Ack(p.SubscriptionID, p.Seq); err != nil {
		return struct{}{}, err
	}
	return struct{}{}, nil
}

// CancelParams is the param shape for kernel.cancel.
type CancelParams struct {
	// RequestID is the client's JSON-RPC request id of an in-flight
	// call this client wants to abort (SPEC §6.23). Accepts either
	// numeric or string ids — the conn-side dispatcher uses the
	// same normalization as the in-flight registry.
	RequestID json.RawMessage `json:"request_id"`
}

// CancelResult reports whether a matching in-flight request was
// found + cancelled. Cancelling an already-finished or unknown
// request is not an error — it just reports cancelled=false.
type CancelResult struct {
	Cancelled bool `json:"cancelled"`
}

// UnsubscribeParams is the param shape for kernel.unsubscribe.
type UnsubscribeParams struct {
	SubscriptionID string `json:"subscription_id"`
}

// Unsubscribe implements kernel.unsubscribe.
func (s *Service) Unsubscribe(ctx context.Context, p UnsubscribeParams) (struct{}, error) {
	if p.SubscriptionID == "" {
		return struct{}{}, fmt.Errorf("unsubscribe: subscription_id required")
	}
	s.subscriptions().Unsubscribe(p.SubscriptionID)
	return struct{}{}, nil
}

// PingResult mirrors daemon.ping but is specific to the kernel
// namespace so monitoring callers can distinguish RPC liveness from
// subscriber liveness. SPEC §6.22 heartbeat clause.
type KernelPingResult struct {
	Pong bool `json:"pong"`
}

// RebuildFromSnapshotParams is the param shape for
// kernel.rebuildFromSnapshot (SPEC §6.22 backpressure recovery).
type RebuildFromSnapshotParams struct {
	SubscriptionID string `json:"subscription_id"`
}

// RebuildFromSnapshotResult is the wire shape returned by
// kernel.rebuildFromSnapshot. Per F1 / plan/answers/06 §Y, this RPC
// is a CHECKPOINT + CURSOR-ADVANCE, not a state capture. The
// `RecoveryHint` field documents the client's next move: re-query
// authoritative state via `Kernel.Route` (selectors.test, code.list,
// etc.) at the new cursor seq. A real per-layer snapshot mechanism
// lands in P3 with the layer-manifest `snapshot:` clauses.
//
// Pre-F1 the wire form used `SnapshotID`/`SnapshotSeq`; we retain
// those keys as deprecated aliases (pointing at the same values) so
// existing clients keep working through one release.
type RebuildFromSnapshotResult struct {
	// CheckpointID is an opaque identifier for the daemon's state at
	// CheckpointSeq. Today it is `cp-seq-<n>`; future P3 snapshots may
	// return a richer handle that LoadSnapshot can rehydrate from.
	CheckpointID string `json:"checkpoint_id"`
	// CheckpointSeq is the daemon's head seq when the cursor was
	// advanced. Clients should re-query authoritative state via
	// `Kernel.Route` pinned at this seq.
	CheckpointSeq uint64 `json:"checkpoint_seq"`
	// NewCursor is the seq the subscription's pump resumes from
	// (i.e. it next delivers seq=NewCursor+1).
	NewCursor uint64 `json:"new_cursor"`
	// RecoveryHint names the client's next move. Today the only
	// value is "requery_via_route"; future P3 snapshot semantics may
	// add "load_snapshot".
	RecoveryHint string `json:"recovery_hint"`

	// Deprecated: SnapshotID / SnapshotSeq are pre-F1 aliases. New
	// clients should read CheckpointID/CheckpointSeq instead.
	SnapshotID  string `json:"snapshot_id,omitempty"`
	SnapshotSeq uint64 `json:"snapshot_seq,omitempty"`
}

// RebuildFromSnapshot implements kernel.rebuildFromSnapshot — the
// recovery half of the SPEC §6.22 backpressure contract.
//
// **Semantics (F1 corrected):** this is a fast-forward, not a state
// capture. The daemon advances the named subscription's cursor to
// the current head and returns the head seq as a checkpoint. The
// subscriber does NOT receive the events between its prior cursor
// and the checkpoint; missed state must be re-fetched authoritatively
// via `Kernel.Route` (selectors.test / code.list / entity.provenance
// / etc.) pinned at the returned CheckpointSeq.
//
// This matches plan/answers/04 §5: "Clients of the kernel speak
// JSON-RPC and re-query state when they fall behind; the kernel does
// not push state, it pushes change signals."
//
// A real per-layer snapshot capture (where the daemon serializes
// code.core state into a portable blob the client can load locally)
// lands in P3 alongside the layer-manifest `snapshot:` clauses.
func (s *Service) RebuildFromSnapshot(_ context.Context, p RebuildFromSnapshotParams) (RebuildFromSnapshotResult, error) {
	if p.SubscriptionID == "" {
		return RebuildFromSnapshotResult{}, fmt.Errorf("rebuildFromSnapshot: subscription_id required")
	}
	headSeq := uint64(0)
	if s.Log != nil {
		headSeq = s.Log.LastSeq()
	}
	if err := s.subscriptions().AdvanceCursor(p.SubscriptionID, headSeq); err != nil {
		return RebuildFromSnapshotResult{}, err
	}
	checkpointID := fmt.Sprintf("cp-seq-%d", headSeq)
	return RebuildFromSnapshotResult{
		CheckpointID:  checkpointID,
		CheckpointSeq: headSeq,
		NewCursor:     headSeq,
		RecoveryHint:  "requery_via_route",
		// Deprecated aliases for pre-F1 clients.
		SnapshotID:  checkpointID,
		SnapshotSeq: headSeq,
	}, nil
}

// KernelPing implements kernel.ping (SPEC §6.22 heartbeat).
// Returning {pong: true} is the wire signal that the subscriber's
// connection is alive — callers that issue ping on an idle timer
// can use the round-trip as a liveness probe and trigger
// reconnection on failure. The daemon-side eviction path uses the
// last-seen time of every conn (touched on every RPC) to evict
// silent subscribers after the configured threshold; eviction
// emits SubscriberEvicted on the kernel bus.
func (s *Service) KernelPing(_ context.Context) (KernelPingResult, error) {
	return KernelPingResult{Pong: true}, nil
}

// registerConn binds method to a connection-aware handler. Mirrors
// Register but threads the conn into the handler so notification
// pushes know which client to address.
func registerConn[P any, R any](s *Server, method, capName string, fn func(ctx context.Context, conn *jsonrpc2.Conn, p P) (R, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.methods[method] = methodEntry{
		cap: capName,
		connHandler: func(ctx context.Context, conn *jsonrpc2.Conn, raw json.RawMessage) (any, error) {
			var p P
			if len(raw) > 0 && string(raw) != "null" {
				if err := json.Unmarshal(raw, &p); err != nil {
					return nil, &jsonrpc2.Error{Code: -32602, Message: fmt.Sprintf("invalid params: %v", err)}
				}
			}
			return fn(ctx, conn, p)
		},
	}
}
