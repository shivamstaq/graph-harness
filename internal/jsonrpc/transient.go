package jsonrpc

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/sourcegraph/jsonrpc2"
)

// SPEC §6.19 source classes that the transient overlay tier accepts.
// These are the names consumer planes (IDE editor buffers, agent
// drafts, mid-form CLI/Studio edits, post-v1 peer-proposed overlay)
// use to identify the kind of draft they're publishing.
const (
	SourceClassEditor      = "editor"
	SourceClassAgentDraft  = "agent_draft"
	SourceClassCLIForm     = "cli_form"
	SourceClassStudioForm  = "studio_form"
	SourceClassTeamPending = "team_pending"
)

// transientSourceClasses lists every accepted source class. The
// publish handler rejects unknown classes so callers can't smuggle
// arbitrary tags through the contract.
var transientSourceClasses = map[string]struct{}{
	SourceClassEditor:      {},
	SourceClassAgentDraft:  {},
	SourceClassCLIForm:     {},
	SourceClassStudioForm:  {},
	SourceClassTeamPending: {},
}

// TransientEntry is one in-memory overlay fact. Payload carries the
// source-class-specific shape (editor buffer text, draft patch
// content, etc.); the kernel doesn't inspect it. The provenance
// fields are stamped on read so consumers (selector resolver,
// `selectors test`, etc.) know they're working off a transient view.
type TransientEntry struct {
	SubscriberID string          `json:"subscriber_id"`
	SourceClass  string          `json:"source_class"`
	TargetID     string          `json:"target_id"`
	Payload      json.RawMessage `json:"payload"`
	// SeqAtPublish is the kernel.LastSeq() observed when this entry
	// was published; useful for ordering against durable facts.
	SeqAtPublish uint64 `json:"seq_at_publish"`
}

// transientKey is the composite-key form (subscriber_id, source_class, target_id).
type transientKey struct {
	subscriberID string
	sourceClass  string
	targetID     string
}

// TransientOverlay is the in-memory tier per SPEC §6.19. It is
// keyed by (subscriber_id, source_class, target_id) and persists no
// durable storage — every entry vanishes when its subscriber
// disconnects or explicitly drops it. The tier enforces a per-
// subscriber LRU cap; on overflow it evicts the oldest entries and
// emits TransientTierOverflow over the kernel bus so observers can
// degrade gracefully.
//
// Read precedence: the tier exposes Get(target_id) which returns
// the most-recent transient claim across every subscriber. Read-
// side integration (selector resolver, code.core lookups merging
// transient > durable) lives in the layers that consume code.core;
// it lands in P3.T01a / P4.T20a / P4.T31a / P4.T36a per the rev's
// per-phase placement.
type TransientOverlay struct {
	cap int

	mu       sync.Mutex
	entries  map[transientKey]*list.Element // key → LRU element
	byTarget map[string][]*list.Element     // target_id → list of (newest first)
	bySub    map[string]map[transientKey]struct{}
	lru      *list.List // LRU ordering, oldest at Back

	// onOverflow fires once per (subscriber_id, source_class) when
	// an LRU eviction is forced. Nil discards.
	onOverflow func(evicted TransientEntry)
}

// DefaultTransientCap is the per-subscription LRU cap (SPEC §6.19).
const DefaultTransientCap = 10_000

// NewTransientOverlay constructs an overlay with the default cap.
func NewTransientOverlay() *TransientOverlay {
	return &TransientOverlay{
		cap:      DefaultTransientCap,
		entries:  map[transientKey]*list.Element{},
		byTarget: map[string][]*list.Element{},
		bySub:    map[string]map[transientKey]struct{}{},
		lru:      list.New(),
	}
}

// SetCap overrides the per-subscriber LRU cap. Must be called
// before any Put.
func (t *TransientOverlay) SetCap(cap int) {
	t.cap = cap
}

// SetOverflowSink registers a callback for evicted entries. The
// callback runs synchronously inside Put; keep it fast or hand off
// to a goroutine.
func (t *TransientOverlay) SetOverflowSink(f func(TransientEntry)) {
	t.onOverflow = f
}

// Put inserts or replaces a transient entry. Returns the prior
// entry at the same key (or zero-valued when none). When the per-
// subscriber count exceeds cap, the oldest entry for THAT subscriber
// is evicted (the onOverflow sink fires for it).
func (t *TransientOverlay) Put(e TransientEntry) (prev TransientEntry, replaced bool) {
	if e.SubscriberID == "" || e.TargetID == "" {
		return TransientEntry{}, false
	}
	key := transientKey{
		subscriberID: e.SubscriberID,
		sourceClass:  e.SourceClass,
		targetID:     e.TargetID,
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	// Replace existing entry at the same key.
	if elt, ok := t.entries[key]; ok {
		prev = elt.Value.(TransientEntry)
		elt.Value = e
		t.lru.MoveToFront(elt)
		return prev, true
	}

	// Insert new entry at the front of the LRU.
	elt := t.lru.PushFront(e)
	t.entries[key] = elt
	if t.bySub[e.SubscriberID] == nil {
		t.bySub[e.SubscriberID] = map[transientKey]struct{}{}
	}
	t.bySub[e.SubscriberID][key] = struct{}{}
	t.byTarget[e.TargetID] = append(t.byTarget[e.TargetID], elt)

	// Per-subscriber LRU eviction.
	if t.cap > 0 && len(t.bySub[e.SubscriberID]) > t.cap {
		t.evictOldestForSub(e.SubscriberID)
	}
	return TransientEntry{}, false
}

// Get returns the most-recent transient entry for target_id across
// every subscriber, or (zero, false) when no entry exists. Bypasses
// the LRU; observation does not touch ordering.
func (t *TransientOverlay) Get(targetID string) (TransientEntry, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	bucket := t.byTarget[targetID]
	if len(bucket) == 0 {
		return TransientEntry{}, false
	}
	// Walk from newest (front of LRU) backwards.
	for i := len(bucket) - 1; i >= 0; i-- {
		if bucket[i].Value == nil {
			continue
		}
		return bucket[i].Value.(TransientEntry), true
	}
	return TransientEntry{}, false
}

// Drop removes a single entry by its composite key. Returns true
// when an entry was actually removed.
func (t *TransientOverlay) Drop(subscriberID, sourceClass, targetID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := transientKey{
		subscriberID: subscriberID,
		sourceClass:  sourceClass,
		targetID:     targetID,
	}
	elt, ok := t.entries[key]
	if !ok {
		return false
	}
	t.removeKey(key, elt)
	return true
}

// DropSubscriber removes every entry tied to the named subscriber.
// Called on disconnect (SPEC §6.19 scope-to-session contract) and
// when an MCP/IDE/Studio session explicitly ends.
func (t *TransientOverlay) DropSubscriber(subscriberID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	keys := t.bySub[subscriberID]
	if len(keys) == 0 {
		return 0
	}
	count := 0
	for k := range keys {
		if elt, ok := t.entries[k]; ok {
			t.removeKey(k, elt)
			count++
		}
	}
	delete(t.bySub, subscriberID)
	return count
}

// ListBySubscriber returns every entry tied to the named subscriber.
// Used for diagnostic surfaces; not on a hot read path.
func (t *TransientOverlay) ListBySubscriber(subscriberID string) []TransientEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	keys := t.bySub[subscriberID]
	out := make([]TransientEntry, 0, len(keys))
	for k := range keys {
		if elt, ok := t.entries[k]; ok {
			out = append(out, elt.Value.(TransientEntry))
		}
	}
	return out
}

func (t *TransientOverlay) evictOldestForSub(subscriberID string) {
	// Walk the LRU back-to-front looking for the oldest entry
	// belonging to the named subscriber. O(N) worst case but cap is
	// bounded so this is fine in practice.
	for elt := t.lru.Back(); elt != nil; elt = elt.Prev() {
		e := elt.Value.(TransientEntry)
		if e.SubscriberID != subscriberID {
			continue
		}
		key := transientKey{
			subscriberID: e.SubscriberID,
			sourceClass:  e.SourceClass,
			targetID:     e.TargetID,
		}
		t.removeKey(key, elt)
		if t.onOverflow != nil {
			t.onOverflow(e)
		}
		return
	}
}

func (t *TransientOverlay) removeKey(key transientKey, elt *list.Element) {
	e := elt.Value.(TransientEntry)
	delete(t.entries, key)
	t.lru.Remove(elt)
	if subBucket, ok := t.bySub[e.SubscriberID]; ok {
		delete(subBucket, key)
		if len(subBucket) == 0 {
			delete(t.bySub, e.SubscriberID)
		}
	}
	bucket := t.byTarget[e.TargetID]
	for i, b := range bucket {
		if b == elt {
			bucket = append(bucket[:i], bucket[i+1:]...)
			break
		}
	}
	if len(bucket) == 0 {
		delete(t.byTarget, e.TargetID)
	} else {
		t.byTarget[e.TargetID] = bucket
	}
}

// --- JSON-RPC method handlers ----------------------------------------------

// PublishTransientParams is the param shape for kernel.publishTransient.
type PublishTransientParams struct {
	SourceClass string          `json:"source_class"`
	TargetID    string          `json:"target_id"`
	Payload     json.RawMessage `json:"payload"`
}

// PublishTransientResult acknowledges the publish; carries the
// subscriber_id the daemon assigned (or the one identified earlier).
type PublishTransientResult struct {
	SubscriberID string `json:"subscriber_id"`
}

// PublishTransient implements kernel.publishTransient — the conn-
// aware insert path. The subscriber_id is the conn's identified (or
// ephemeral) id from the SubscriptionManager.
func (s *Service) PublishTransient(_ context.Context, conn *jsonrpc2.Conn, p PublishTransientParams) (PublishTransientResult, error) {
	if conn == nil {
		return PublishTransientResult{}, fmt.Errorf("publishTransient: nil connection")
	}
	if _, ok := transientSourceClasses[p.SourceClass]; !ok {
		return PublishTransientResult{}, fmt.Errorf("publishTransient: unknown source_class %q (allowed: editor, agent_draft, cli_form, studio_form, team_pending)", p.SourceClass)
	}
	if p.TargetID == "" {
		return PublishTransientResult{}, fmt.Errorf("publishTransient: target_id required")
	}
	subID := s.subscriptions().SubscriberIDFor(conn)
	seq := uint64(0)
	if s.Log != nil {
		seq = s.Log.LastSeq()
	}
	s.transient().Put(TransientEntry{
		SubscriberID: subID,
		SourceClass:  p.SourceClass,
		TargetID:     p.TargetID,
		Payload:      p.Payload,
		SeqAtPublish: seq,
	})
	return PublishTransientResult{SubscriberID: subID}, nil
}

// DropTransientParams is the param shape for kernel.dropTransient.
type DropTransientParams struct {
	SourceClass string `json:"source_class"`
	TargetID    string `json:"target_id"`
}

// DropTransient implements kernel.dropTransient — removes a single
// entry the conn previously published. Idempotent (returns no error
// when no entry matches).
func (s *Service) DropTransient(_ context.Context, conn *jsonrpc2.Conn, p DropTransientParams) (struct{}, error) {
	if conn == nil {
		return struct{}{}, fmt.Errorf("dropTransient: nil connection")
	}
	subID := s.subscriptions().SubscriberIDFor(conn)
	s.transient().Drop(subID, p.SourceClass, p.TargetID)
	return struct{}{}, nil
}

// GetTransientParams is the param shape for kernel.getTransient.
type GetTransientParams struct {
	TargetID string `json:"target_id"`
}

// GetTransientResult returns the most-recent transient entry for a
// target, or Found=false when none exists.
type GetTransientResult struct {
	Found bool           `json:"found"`
	Entry TransientEntry `json:"entry,omitempty"`
}

// GetTransient implements kernel.getTransient — diagnostic read of
// the in-memory tier. Read-side merge into code.core / selector
// resolution lives in the consumer phases (P3.T01a / P4.T20a /
// P4.T31a / P4.T36a per answer-07).
func (s *Service) GetTransient(_ context.Context, p GetTransientParams) (GetTransientResult, error) {
	e, ok := s.transient().Get(p.TargetID)
	return GetTransientResult{Found: ok, Entry: e}, nil
}
