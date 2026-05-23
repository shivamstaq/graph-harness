package code_framework

import (
	"context"
	"errors"
	"sync"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// SelectorResolver is the indirection between the EntityRefCache and
// the semantic_overlay resolver. Implemented in the daemon's wiring
// code; mocked in tests. Kept here as an interface so the
// code_framework package does not import semantic_overlay (avoiding
// an import cycle now that the resolver depends on the reverse index
// added in Pass 0.5).
type SelectorResolver interface {
	// Resolve takes a SelectorRef plus the consistency seq the
	// caller wants to read at, and returns the resolved EntityRef
	// or an error if no rung of the anchor ladder matched above its
	// threshold. The semantics mirror semantic_overlay.Resolver.Resolve
	// but for the closed-form SelectorRef carried in framework events.
	Resolve(ctx context.Context, sel SelectorRef, atSeq uint64) (resolved EntityRef, err error)
}

// EntityRef wraps kernel.EntityRef with the resolution metadata the
// cache stores for explain-trace surfacing.
type EntityRef struct {
	Ref           kernel.EntityRef
	Confidence    float64
	ViaAnchor     string
	ResolvedAtSeq uint64
}

// ErrCacheMiss is returned when the cache has no entry for a
// SelectorRef and no SelectorResolver has been provided. Tests use
// it as a sentinel; production code always provides a resolver.
var ErrCacheMiss = errors.New("entity-ref cache miss")

// EntityRefCache memoizes SelectorRef → EntityRef resolutions for the
// Dispatcher's lifetime. Per-Dispatcher (one per workspace) so
// duplicate selectors across extractors hit the same entry once.
//
// Invalidation is driven by code.core drift events: the Dispatcher
// subscribes to FileChanged / FileRemoved / EntityMaterialized /
// EntitySuperseded / SymbolDisambiguation and calls
// InvalidateByPath / InvalidateByEntity as appropriate.
type EntityRefCache struct {
	mu       sync.RWMutex
	entries  map[[32]byte]EntityRef
	byPath   map[string]map[[32]byte]struct{}
	byEntity map[string]map[[32]byte]struct{}
	resolver SelectorResolver

	flightMu sync.Mutex
	flight   map[[32]byte]*flightCall
}

type flightCall struct {
	done chan struct{}
	ref  EntityRef
	err  error
}

// NewEntityRefCache builds an empty cache backed by resolver. Passing
// a nil resolver yields a read-through-only cache useful in tests; in
// production the Dispatcher always wires the semantic_overlay
// resolver in.
func NewEntityRefCache(resolver SelectorResolver) *EntityRefCache {
	return &EntityRefCache{
		entries:  make(map[[32]byte]EntityRef),
		byPath:   make(map[string]map[[32]byte]struct{}),
		byEntity: make(map[string]map[[32]byte]struct{}),
		resolver: resolver,
		flight:   make(map[[32]byte]*flightCall),
	}
}

// Resolve returns the EntityRef for sel at atSeq. Cache hits are
// O(1); misses populate the cache via the resolver (singleflight
// per SelectorRef hash so concurrent callers don't double-resolve).
func (c *EntityRefCache) Resolve(ctx context.Context, sel SelectorRef, atSeq uint64) (EntityRef, error) {
	key := sel.Hash()

	c.mu.RLock()
	if hit, ok := c.entries[key]; ok {
		c.mu.RUnlock()
		return hit, nil
	}
	c.mu.RUnlock()

	// Singleflight: at most one in-flight Resolve per key.
	c.flightMu.Lock()
	if call, ok := c.flight[key]; ok {
		c.flightMu.Unlock()
		select {
		case <-call.done:
			return call.ref, call.err
		case <-ctx.Done():
			return EntityRef{}, ctx.Err()
		}
	}
	call := &flightCall{done: make(chan struct{})}
	c.flight[key] = call
	c.flightMu.Unlock()

	defer func() {
		close(call.done)
		c.flightMu.Lock()
		delete(c.flight, key)
		c.flightMu.Unlock()
	}()

	if c.resolver == nil {
		call.err = ErrCacheMiss
		return EntityRef{}, ErrCacheMiss
	}
	ref, err := c.resolver.Resolve(ctx, sel, atSeq)
	if err != nil {
		call.err = err
		return EntityRef{}, err
	}
	call.ref = ref

	// Promote to the cache + secondary indexes.
	c.put(key, sel, ref)
	return ref, nil
}

// put inserts the entry into the cache and secondary indexes.
// Caller MUST NOT hold c.mu.
func (c *EntityRefCache) put(key [32]byte, sel SelectorRef, ref EntityRef) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = ref
	// Secondary index by entity id (for invalidation on
	// EntityMaterialized / EntitySuperseded).
	if ref.Ref.ID != "" {
		m, ok := c.byEntity[ref.Ref.ID]
		if !ok {
			m = make(map[[32]byte]struct{})
			c.byEntity[ref.Ref.ID] = m
		}
		m[key] = struct{}{}
	}
	// Secondary index by path (for invalidation on FileChanged /
	// FileRemoved). Extract the path from a path_glob or file
	// anchor if present in the SelectorRef.
	for _, a := range sel.Anchors {
		if a.Kind == "path_glob" || a.Kind == "file" {
			m, ok := c.byPath[a.Value]
			if !ok {
				m = make(map[[32]byte]struct{})
				c.byPath[a.Value] = m
			}
			m[key] = struct{}{}
			break
		}
	}
}

// InvalidateByEntity drops every cached entry whose resolved
// EntityRef.ID matches id. Called by the Dispatcher on
// EntityMaterialized / EntitySuperseded / SymbolDisambiguation
// events.
func (c *EntityRefCache) InvalidateByEntity(id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := c.byEntity[id]
	for k := range keys {
		delete(c.entries, k)
	}
	delete(c.byEntity, id)
}

// InvalidateByPath drops every cached entry whose SelectorRef
// referenced path (via a path_glob or file anchor). Called by the
// Dispatcher on FileChanged / FileRemoved events.
func (c *EntityRefCache) InvalidateByPath(path string) {
	if path == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := c.byPath[path]
	for k := range keys {
		delete(c.entries, k)
	}
	delete(c.byPath, path)
}

// InvalidateAll drops every entry. Used on cold start / manifest
// schema migrations / disable-then-enable cycles.
func (c *EntityRefCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[[32]byte]EntityRef)
	c.byEntity = make(map[string]map[[32]byte]struct{})
	c.byPath = make(map[string]map[[32]byte]struct{})
}

// Len returns the number of cached entries. Test-only convenience.
func (c *EntityRefCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
