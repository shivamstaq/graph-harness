package jsonrpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// SubscriptionManager owns the per-connection event-pump goroutines
// that translate facts.EventLog Subscribe streams into JSON-RPC
// notifications pushed back to the requesting client. One manager
// per Service.
//
// Lifecycle (SPEC §6.22 long-lived subscriber contract):
//
//   - kernel.identify({subscriber_id}) — optional; binds a stable
//     client_id to the live conn so cursor state can survive
//     reconnect. When omitted, the manager assigns an ephemeral id.
//   - kernel.subscribe({filter, cursor?}) — registers a subscription;
//     spawns a goroutine that reads from facts.EventLog and emits
//     `kernel.event` notifications. Returns {subscription_id}.
//   - kernel.ack({subscription_id, seq}) — advances the persistent
//     cursor for the subscription (so reconnect resumes from seq+1).
//   - kernel.unsubscribe({subscription_id}) — drops the pump.
//   - Connection close — drops every subscription bound to the conn
//     via DropConn (called from serveConn's disconnect path).
//
// Backpressure (P0.5.T04 / SPEC §6.22): per-subscription queue
// depth defaults to 10K events. On overflow the manager emits a
// `kernel.fellBehind` notification carrying the available
// degradation options; the underlying event channel is the same one
// facts.EventLog provides (subscriber-side drop is at the kernel
// level today, server-push backpressure is layered on top).
type SubscriptionManager struct {
	log *facts.EventLog

	mu      sync.Mutex
	subs    map[string]*activeSub
	byConn  map[*jsonrpc2.Conn]map[string]struct{}
	clients map[*jsonrpc2.Conn]string // conn → subscriber_id (identified or ephemeral)

	// lastSeen tracks per-conn last-activity timestamp. Touched on
	// every RPC; the eviction sweep reads it to drop silent
	// subscribers after the idle threshold (SPEC §6.22 heartbeat).
	lastSeen map[*jsonrpc2.Conn]time.Time
	// idleThreshold is the eviction window; zero disables eviction.
	// Default 24h per SPEC §6.22.
	idleThreshold time.Duration
}

// activeSub is the internal bookkeeping for a single live subscription.
type activeSub struct {
	id           string
	subscriberID string
	filter       string
	conn         *jsonrpc2.Conn
	stream       facts.EventStream

	// cursor is updated atomically every time an event is pushed; ack
	// reads it for persistence. ackedSeq lags the head until the
	// client confirms; that gap is the backpressure window.
	cursor     atomic.Uint64
	ackedSeq   atomic.Uint64
	startSeq   uint64
	queueCap   int
	fellBehind atomic.Bool

	cancel context.CancelFunc
	done   chan struct{}
}

// NewSubscriptionManager builds a manager bound to the given event log.
func NewSubscriptionManager(log *facts.EventLog) *SubscriptionManager {
	return &SubscriptionManager{
		log:           log,
		subs:          map[string]*activeSub{},
		byConn:        map[*jsonrpc2.Conn]map[string]struct{}{},
		clients:       map[*jsonrpc2.Conn]string{},
		lastSeen:      map[*jsonrpc2.Conn]time.Time{},
		idleThreshold: defaultIdleEviction,
	}
}

// defaultIdleEviction is the SPEC §6.22 default (24h). High enough
// that long-lived editor sessions don't trip it, low enough that
// pidfile-only zombies clear eventually.
const defaultIdleEviction = 24 * time.Hour

// SetIdleThreshold overrides the eviction window. Zero disables it
// (tests use 0 to keep subscriptions alive across sleeps).
func (m *SubscriptionManager) SetIdleThreshold(d time.Duration) {
	m.mu.Lock()
	m.idleThreshold = d
	m.mu.Unlock()
}

// Touch records activity for conn. Called by the dispatcher on every
// RPC so the eviction sweep sees recent liveness. kernel.ping exists
// specifically so idle long-lived subscribers can stay marked-live
// without issuing functional RPCs.
func (m *SubscriptionManager) Touch(conn *jsonrpc2.Conn) {
	if conn == nil {
		return
	}
	m.mu.Lock()
	m.lastSeen[conn] = time.Now()
	m.mu.Unlock()
}

// StartEvictionLoop runs the periodic sweep until ctx is cancelled.
// Tick defaults to idleThreshold/4 with a 1s floor.
func (m *SubscriptionManager) StartEvictionLoop(ctx context.Context, tickInterval time.Duration) {
	m.mu.Lock()
	threshold := m.idleThreshold
	m.mu.Unlock()
	if threshold <= 0 {
		return
	}
	if tickInterval <= 0 {
		tickInterval = threshold / 4
	}
	if tickInterval < time.Second {
		tickInterval = time.Second
	}
	go func() {
		t := time.NewTicker(tickInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.evictIdleConns()
			}
		}
	}()
}

// evictIdleConns is one sweep. Drops conns whose lastSeen exceeds
// the threshold, tears down their subscriptions (cursors persist
// via the Ack path), closes the conn, and emits SubscriberEvicted
// on the kernel bus so monitoring consumers (TUI / Studio) surface
// the loss.
func (m *SubscriptionManager) evictIdleConns() {
	m.mu.Lock()
	threshold := m.idleThreshold
	if threshold <= 0 {
		m.mu.Unlock()
		return
	}
	now := time.Now()
	type victim struct {
		conn         *jsonrpc2.Conn
		subscriberID string
		subIDs       []string
	}
	var victims []victim
	for conn, last := range m.lastSeen {
		if now.Sub(last) < threshold {
			continue
		}
		v := victim{conn: conn, subscriberID: m.clients[conn]}
		for subID := range m.byConn[conn] {
			v.subIDs = append(v.subIDs, subID)
		}
		victims = append(victims, v)
	}
	m.mu.Unlock()
	for _, v := range victims {
		for _, subID := range v.subIDs {
			m.Unsubscribe(subID)
		}
		if v.conn != nil {
			_ = v.conn.Close()
		}
		m.mu.Lock()
		delete(m.lastSeen, v.conn)
		delete(m.clients, v.conn)
		delete(m.byConn, v.conn)
		m.mu.Unlock()
		if m.log != nil {
			payload, _ := json.Marshal(map[string]any{
				"subscriber_id":   v.subscriberID,
				"subscription_id": v.subIDs,
				"reason":          "idle_timeout",
			})
			_, _ = m.log.Append(context.Background(), []kernel.Event{{
				Layer:      "kernel.subscriptions",
				Kind:       "SubscriberEvicted",
				Payload:    payload,
				ProducedBy: kernel.SourceLayerInternal,
			}})
		}
	}
}

// Identify binds a stable subscriber_id to conn. Subsequent calls on
// the same conn replace the binding; the prior subscriptions stay
// alive but their cursor key remains the old subscriber_id (so re-
// identifying mid-session does not migrate cursors).
func (m *SubscriptionManager) Identify(conn *jsonrpc2.Conn, subscriberID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if subscriberID == "" {
		// Ephemeral id when client opts out of stable identity.
		subscriberID = "anon-" + randomID()
	}
	m.clients[conn] = subscriberID
}

// SubscriberIDFor returns the binding for conn, allocating an
// ephemeral one if none exists. Called by Subscribe so every
// subscription has a non-empty subscriber_id.
func (m *SubscriptionManager) SubscriberIDFor(conn *jsonrpc2.Conn) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.clients[conn]; ok {
		return id
	}
	id := "anon-" + randomID()
	m.clients[conn] = id
	return id
}

// Subscribe registers a subscription with the default queue cap.
// Use SubscribeWithOpts to override the cap.
func (m *SubscriptionManager) Subscribe(ctx context.Context, conn *jsonrpc2.Conn, filterExpr string, cursor uint64) (string, error) {
	return m.SubscribeWithOpts(ctx, conn, filterExpr, cursor, 0)
}

// SubscribeWithOpts is Subscribe with an explicit per-subscription
// queue cap. queueCap <= 0 falls back to defaultQueueDepth.
// Returns the subscription_id the client uses to ack / unsubscribe.
//
// The pump goroutine runs under a context derived from
// context.Background(), NOT the caller's ctx — the caller's ctx is
// the per-request ctx and gets cancelled when kernel.subscribe
// returns, which would tear down the pump immediately. Lifetime is
// instead bound to the connection: DropConn cancels the pump on
// disconnect, or Unsubscribe cancels it explicitly.
func (m *SubscriptionManager) SubscribeWithOpts(ctx context.Context, conn *jsonrpc2.Conn, filterExpr string, cursor uint64, queueCap int) (string, error) {
	if conn == nil {
		return "", fmt.Errorf("subscribe: nil connection")
	}
	if m.log == nil {
		return "", fmt.Errorf("subscribe: event log not available")
	}

	// Parse filter; empty string == match-all.
	filter, err := parseFilter(filterExpr)
	if err != nil {
		return "", fmt.Errorf("parse filter: %w", err)
	}

	subID := "sub-" + randomID()
	stream := m.log.SubscribeWithFilter(filter)
	subscriberID := m.SubscriberIDFor(conn)
	// SPEC §6.22 reconnect: when the client doesn't supply a cursor,
	// resume from the persisted last_processed_seq for this
	// subscriber. Zero means start at head (default).
	if cursor == 0 {
		cursor = m.CursorOf(ctx, subscriberID)
	}
	// Decouple the pump from the caller's per-request ctx. The pump
	// lives as long as the subscription does — either Unsubscribe()
	// or DropConn() invokes cancel(); the request-bound ctx going
	// away (which happens immediately after kernel.subscribe
	// returns) MUST NOT tear it down.
	subCtx, cancel := context.WithCancel(context.Background())
	_ = ctx

	if queueCap <= 0 {
		queueCap = defaultQueueDepth
	}
	as := &activeSub{
		id:           subID,
		subscriberID: subscriberID,
		filter:       filterExpr,
		conn:         conn,
		stream:       stream,
		startSeq:     cursor,
		queueCap:     queueCap,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
	as.cursor.Store(cursor)
	as.ackedSeq.Store(cursor)

	m.mu.Lock()
	m.subs[subID] = as
	bucket, ok := m.byConn[conn]
	if !ok {
		bucket = map[string]struct{}{}
		m.byConn[conn] = bucket
	}
	bucket[subID] = struct{}{}
	m.mu.Unlock()

	go m.pump(subCtx, as)
	return subID, nil
}

// Unsubscribe cancels the named subscription. Idempotent.
func (m *SubscriptionManager) Unsubscribe(subID string) {
	m.mu.Lock()
	as, ok := m.subs[subID]
	delete(m.subs, subID)
	if ok && as != nil {
		if bucket := m.byConn[as.conn]; bucket != nil {
			delete(bucket, subID)
		}
	}
	m.mu.Unlock()
	if !ok || as == nil {
		return
	}
	as.cancel()
	_ = as.stream.Close()
	<-as.done
}

// Ack advances the cursor for subID up to seq. Reconnect after ack
// resumes from seq+1. When the un-acked gap shrinks back under the
// queue cap, the fellBehind one-shot flag clears so subsequent
// overflows can fire a fresh notification.
//
// P0.5.T02/T03 cross-restart persistence: the ack also writes the
// new last_processed_seq into the kernel-owned layer_state table via
// EventLog.AdvanceCursor, keyed by the subscriber's stable identity
// (subscriber_id || ephemeral). Restart of the daemon + reconnect
// then resumes from seq+1 instead of head — closing the audit gap
// where Ack was only in-memory.
func (m *SubscriptionManager) Ack(subID string, seq uint64) error {
	m.mu.Lock()
	as, ok := m.subs[subID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("ack: subscription %s not found", subID)
	}
	// Monotonic; refuse acks that go backwards.
	prev := as.ackedSeq.Load()
	if seq < prev {
		return fmt.Errorf("ack: seq %d is behind acked %d", seq, prev)
	}
	as.ackedSeq.Store(seq)
	if int(as.cursor.Load()-as.ackedSeq.Load()) < as.queueCap {
		as.fellBehind.Store(false)
	}
	// Persist the cursor so reconnect-across-daemon-restart can
	// resume. Best-effort: a write failure does not fail the ack
	// (the in-memory state is still correct for this session).
	if m.log != nil && as.subscriberID != "" {
		_ = m.log.AdvanceCursor(context.Background(), as.subscriberID, seq)
	}
	return nil
}

// CursorOf returns the persisted last_processed_seq for the given
// subscriber identity, or zero if none has been recorded. Used by
// SubscribeWithOpts when the client omits an explicit cursor — the
// subscription resumes from the persisted seq+1 instead of head, per
// SPEC §6.22 reconnect contract.
func (m *SubscriptionManager) CursorOf(ctx context.Context, subscriberID string) uint64 {
	if m.log == nil || subscriberID == "" {
		return 0
	}
	seq, err := m.log.CursorOf(ctx, subscriberID)
	if err != nil {
		return 0
	}
	return seq
}

// DropConn drops every subscription tied to conn. Called from the
// serveConn disconnect path.
func (m *SubscriptionManager) DropConn(conn *jsonrpc2.Conn) {
	m.mu.Lock()
	bucket := m.byConn[conn]
	delete(m.byConn, conn)
	delete(m.clients, conn)
	ids := make([]string, 0, len(bucket))
	for id := range bucket {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Unsubscribe(id)
	}
}

// pump is the per-subscription goroutine: it reads events from the
// underlying facts.EventStream and pushes them to the client as
// `kernel.event` JSON-RPC notifications. On context cancellation
// or stream close it exits cleanly.
func (m *SubscriptionManager) pump(ctx context.Context, as *activeSub) {
	defer close(as.done)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-as.stream.Events():
			if !ok {
				return
			}
			if as.startSeq > 0 && ev.Seq <= as.startSeq {
				continue
			}
			// Backpressure (SPEC §6.22): the un-acked gap is
			// (cursor - ackedSeq). When it crosses queueCap the
			// client is behind: fire a one-shot kernel.fellBehind
			// notification with the recovery options. Subsequent
			// overflow on the same subscription is suppressed until
			// an ack moves the gap back under cap (handled by Ack
			// clearing the fellBehind flag).
			gap := int(as.cursor.Load() - as.ackedSeq.Load())
			if gap >= as.queueCap && !as.fellBehind.Swap(true) {
				_ = as.conn.Notify(ctx, "kernel.fellBehind", FellBehindNotification{
					SubscriptionID: as.id,
					QueueDepth:     gap,
					QueueCapacity:  as.queueCap,
					Options:        []string{"rebuild_from_snapshot", "skip_ahead_with_loss", "pause_for_catchup"},
				})
			}
			n := EventNotification{
				SubscriptionID: as.id,
				SubscriberID:   as.subscriberID,
				Seq:            ev.Seq,
				Layer:          ev.Layer,
				Kind:           ev.Kind,
				Payload:        ev.Payload,
			}
			if err := as.conn.Notify(ctx, "kernel.event", n); err != nil {
				// Connection died mid-push — stop pumping.
				return
			}
			as.cursor.Store(ev.Seq)
		}
	}
}

// EventNotification is the wire shape for the `kernel.event`
// notification the daemon pushes to each subscriber.
type EventNotification struct {
	SubscriptionID string          `json:"subscription_id"`
	SubscriberID   string          `json:"subscriber_id"`
	Seq            uint64          `json:"seq"`
	Layer          string          `json:"layer"`
	Kind           string          `json:"kind"`
	Payload        json.RawMessage `json:"payload,omitempty"`
}

// FellBehindNotification is the wire shape for SPEC §6.22's
// backpressure-overflow event. Sent once per subscription that
// crosses the queue-depth threshold; subsequent overflow events on
// the same subscription are suppressed until the client recovers.
type FellBehindNotification struct {
	SubscriptionID string   `json:"subscription_id"`
	QueueDepth     int      `json:"queue_depth"`
	QueueCapacity  int      `json:"queue_capacity"`
	Options        []string `json:"options"` // rebuild_from_snapshot / skip_ahead_with_loss / pause_for_catchup
}

// defaultQueueDepth matches SPEC §6.22's default per-subscription
// queue depth. Configurable per subscription via a future opts param;
// 10K events comfortably absorbs a several-minute editor session
// at high event cadence.
const defaultQueueDepth = 10_000

func randomID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// parseFilter turns a Participle filter expression into the
// kernel.EventFilter the event log uses. Empty string yields the
// match-all filter. Full grammar parity with the layer-manifest
// filter clause lands when the DSL parser exposes its public form
// here; for now we accept layer-or-(layer.kind) shorthand which is
// the only form the P0.5 demo + subscriber-roundtrip test needs.
func parseFilter(expr string) (kernel.EventFilter, error) {
	if expr == "" {
		return kernel.EventFilter{}, nil
	}
	f := kernel.EventFilter{}
	// Support "layer" and "layer/kind" shorthand (e.g. "code.core",
	// "code.core/FileChanged"). The grammar will widen to the
	// Participle dialect once that is plumbed through; for now this
	// keeps the surface minimal but useful.
	if i := indexByte(expr, '/'); i > 0 {
		f.Layers = []string{expr[:i]}
		f.Kinds = []string{expr[i+1:]}
	} else {
		f.Layers = []string{expr}
	}
	return f, nil
}

func indexByte(s string, c byte) int {
	for i := range len(s) {
		if s[i] == c {
			return i
		}
	}
	return -1
}
