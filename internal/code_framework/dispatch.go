package code_framework

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// ExtractorStatus is the live status row returned by ExtractorsStatus
// JSON-RPC. Snapshot-shaped — readers do not see partial updates.
type ExtractorStatus struct {
	Name       string       `json:"name"`
	Family     string       `json:"family"`
	Languages  []string     `json:"languages"`
	Frameworks []string     `json:"frameworks"`
	Inputs     []EventKind  `json:"inputs"`
	Outputs    []EntityKind `json:"outputs"`
	Enabled    bool         `json:"enabled"`
	LastSeq    uint64       `json:"last_seq"`
	EventsIn   uint64       `json:"events_in"`
	EventsOut  uint64       `json:"events_out"`
	Errors     uint64       `json:"errors"`
	LastError  string       `json:"last_error,omitempty"`
}

// dispatchedEntry is a per-extractor live state record held by the
// Dispatcher.
type dispatchedEntry struct {
	desc Descriptor
	ext  Extractor // nil until Enabled; constructed on first enable

	enabled atomic.Bool

	// Counters (atomics so Status() is lock-free).
	lastSeq   atomic.Uint64
	eventsIn  atomic.Uint64
	eventsOut atomic.Uint64
	errors    atomic.Uint64

	// LastError under a mutex (string, not atomic).
	errMu     sync.Mutex
	lastError string
}

// EventEmitter is the indirection the Dispatcher uses to write events
// back to the kernel event log. In production this is the daemon-
// owned *facts.EventLog; in tests it is a stub.
type EventEmitter interface {
	Append(ctx context.Context, events []kernel.Event) (uint64, error)
}

// StateReader provides last-known content-hash lookups for
// compare-before-emit. The Dispatcher consults it before appending a
// proposed event: if the stored row's content hash matches the
// proposed payload's content hash, the emission is suppressed.
// In Pass 0 this is satisfied by an in-memory map (the Dispatcher's
// own writes feed back through OnEvent for downstream consumers but
// also through this StateReader for self-suppression on idempotent
// re-extracts). Pass 1 swaps in a SQLite-backed reader.
type StateReader interface {
	ContentHash(ctx context.Context, layer, kind, id string) (string, bool, error)
}

// memStateReader is the Pass-0 in-memory StateReader. Pass 1's
// SQLite-backed Store replaces it.
type memStateReader struct {
	mu    sync.RWMutex
	hashes map[string]string // "<layer>/<kind>/<id>" -> content hash
}

// NewMemStateReader builds an in-memory StateReader.
func NewMemStateReader() *memStateReader { //nolint:revive // exposing concrete type for direct manipulation in tests
	return &memStateReader{hashes: make(map[string]string)}
}

// ContentHash returns the stored hash for the entity ref.
func (m *memStateReader) ContentHash(_ context.Context, layer, kind, id string) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.hashes[layer+"/"+kind+"/"+id]
	return v, ok, nil
}

// Put updates the stored hash. Called by the Dispatcher after a
// successful Append.
func (m *memStateReader) Put(layer, kind, id, hash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hashes[layer+"/"+kind+"/"+id] = hash
}

// DispatcherConfig is the wiring for a new Dispatcher.
type DispatcherConfig struct {
	Workspace string
	Facts     facts.Facts
	EventLog  EventEmitter
	State     StateReader
	Resolver  SelectorResolver
	Config    *Config // nil = treat as everything enabled
	Logf      func(string, ...any)
}

// Dispatcher routes input events to enabled extractors and appends
// their emissions to the kernel event log after compare-before-emit
// suppression. One per workspace; owned by the daemon.
type Dispatcher struct {
	cfg   DispatcherConfig
	cache *EntityRefCache

	mu          sync.RWMutex
	entries     map[string]*dispatchedEntry
	enabledMu   sync.RWMutex
	byInputKind map[EventKind][]*dispatchedEntry
	started     atomic.Bool
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

// NewDispatcher constructs a Dispatcher seeded with every extractor
// in the package-level registry. Extractors are instantiated lazily
// in Start (so the registry can be reset by tests between Dispatcher
// constructions).
func NewDispatcher(cfg DispatcherConfig) (*Dispatcher, error) {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.State == nil {
		cfg.State = NewMemStateReader()
	}
	d := &Dispatcher{
		cfg:         cfg,
		cache:       NewEntityRefCache(cfg.Resolver),
		entries:     make(map[string]*dispatchedEntry),
		byInputKind: make(map[EventKind][]*dispatchedEntry),
	}
	for _, desc := range Descriptors() {
		entry := &dispatchedEntry{desc: desc}
		entry.enabled.Store(cfg.Config == nil || !cfg.Config.IsDisabled(desc.Name))
		d.entries[desc.Name] = entry
	}
	d.rebuildIndex()
	return d, nil
}

// Cache exposes the EntityRefCache for surfaces that need to query
// it (Studio impact view, IDE hover) outside the OnEvent path.
func (d *Dispatcher) Cache() *EntityRefCache { return d.cache }

// Start opens the event-log subscription and begins routing events
// to enabled extractors. Returns after the subscription is wired but
// before any events have been processed (subscription is consumed
// in a goroutine).
//
// Idempotent: calling Start twice is a no-op.
func (d *Dispatcher) Start(ctx context.Context) error {
	if !d.started.CompareAndSwap(false, true) {
		return nil
	}
	if d.cfg.Facts == nil {
		return fmt.Errorf("dispatcher: Facts handle required")
	}
	if d.cfg.EventLog == nil {
		return fmt.Errorf("dispatcher: EventLog required")
	}

	// Construct enabled extractors now so we can fail fast on bad
	// constructors.
	d.mu.Lock()
	for _, e := range d.entries {
		if !e.enabled.Load() {
			continue
		}
		if err := d.constructLocked(e); err != nil {
			d.mu.Unlock()
			return err
		}
	}
	d.mu.Unlock()
	d.rebuildIndex()

	subCtx, cancel := context.WithCancel(ctx)
	d.cancel = cancel

	filter := d.computeFilter()
	stream, err := d.cfg.Facts.Subscribe(subCtx, filter)
	if err != nil {
		cancel()
		return fmt.Errorf("subscribe events: %w", err)
	}
	d.wg.Add(1)
	go d.loop(subCtx, stream)
	return nil
}

// Stop shuts down the dispatcher. Idempotent.
func (d *Dispatcher) Stop(_ context.Context) error {
	if !d.started.Load() {
		return nil
	}
	if d.cancel != nil {
		d.cancel()
	}
	d.wg.Wait()
	d.started.Store(false)
	return nil
}

// loop consumes events from the subscription and fans them out.
func (d *Dispatcher) loop(ctx context.Context, stream facts.EventStream) {
	defer d.wg.Done()
	defer func() { _ = stream.Close() }()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-stream.Events():
			if !ok {
				return
			}
			d.routeOne(ctx, ev)
		}
	}
}

// routeOne is the per-event fan-out: look up extractors that
// subscribed to the event's "<layer>.<kind>" and invoke OnEvent
// concurrently with a bounded worker shape (default: one goroutine
// per extractor for the event, which is the simplest correct shape
// in Pass 0; Pass 6 perf gate may add a bounded pool).
func (d *Dispatcher) routeOne(ctx context.Context, ev kernel.Event) {
	kind := EventKind(ev.Layer + "." + ev.Kind)

	// Cache invalidation hooks: drift events drop matching entries
	// *before* extractors observe the event so their re-resolution
	// during OnEvent sees a fresh result.
	d.maybeInvalidate(ev)

	d.enabledMu.RLock()
	targets := append([]*dispatchedEntry(nil), d.byInputKind[kind]...)
	d.enabledMu.RUnlock()

	if len(targets) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, e := range targets {
		if !e.enabled.Load() || e.ext == nil {
			continue
		}
		wg.Add(1)
		go func(e *dispatchedEntry) {
			defer wg.Done()
			d.invokeOne(ctx, e, ev)
		}(e)
	}
	wg.Wait()
}

// invokeOne calls one extractor's OnEvent, stamps the returned
// events, and appends them to the kernel log after compare-before-
// emit suppression.
func (d *Dispatcher) invokeOne(ctx context.Context, e *dispatchedEntry, in kernel.Event) {
	e.eventsIn.Add(1)
	e.lastSeq.Store(in.Seq)

	out, err := e.ext.OnEvent(ctx, in)
	if err != nil {
		e.errors.Add(1)
		e.errMu.Lock()
		e.lastError = err.Error()
		e.errMu.Unlock()
		d.cfg.Logf("extractor %s OnEvent: %v", e.desc.Name, err)
		return
	}
	if len(out) == 0 {
		return
	}

	// Stamp + compare-before-emit. Pass 0 compares on a
	// per-event payload hash because we don't yet have the
	// framework-Store schema; Pass 1 swaps in entity-id-keyed row
	// comparison.
	stamped := make([]kernel.Event, 0, len(out))
	producedBy := kernel.SourceClass(string(SourceExtractorFramework) + ":" + e.desc.Name)
	for _, p := range out {
		p.Layer = "code.framework"
		if p.ProducedBy == "" {
			p.ProducedBy = producedBy
		}
		if p.Tx == "" {
			p.Tx = in.Tx
		}
		p.Causes = append(p.Causes, in.Seq)
		hash, id, kindStr := payloadHashFor(p)
		if !d.shouldEmit(ctx, p.Layer, kindStr, id, hash) {
			continue
		}
		stamped = append(stamped, p)
	}
	if len(stamped) == 0 {
		return
	}
	if _, err := d.cfg.EventLog.Append(ctx, stamped); err != nil {
		e.errors.Add(1)
		e.errMu.Lock()
		e.lastError = err.Error()
		e.errMu.Unlock()
		d.cfg.Logf("extractor %s Append: %v", e.desc.Name, err)
		return
	}
	// Promote the stored hashes after successful append.
	if sw, ok := d.cfg.State.(*memStateReader); ok {
		for _, p := range stamped {
			hash, id, kindStr := payloadHashFor(p)
			if hash != "" && id != "" {
				sw.Put(p.Layer, kindStr, id, hash)
			}
		}
	}
	e.eventsOut.Add(uint64(len(stamped)))
}

// shouldEmit returns true if the proposed event differs from the
// stored row (compare-before-emit, SPEC §6.21). Events with no id
// in their payload are always emitted (e.g. UnverifiedGeneratedArtifact
// which is a workspace-level notice).
func (d *Dispatcher) shouldEmit(ctx context.Context, layer, kind, id, hash string) bool {
	if id == "" || hash == "" {
		return true
	}
	prev, ok, err := d.cfg.State.ContentHash(ctx, layer, kind, id)
	if err != nil {
		// Errors fall through to emit — surface as a duplicate
		// rather than silently dropping facts.
		return true
	}
	if !ok {
		return true
	}
	return prev != hash
}

// maybeInvalidate drops EntityRefCache entries when drift arrives.
func (d *Dispatcher) maybeInvalidate(ev kernel.Event) {
	switch ev.Kind {
	case "FileChanged", "FileRemoved":
		// Payload carries `path` per code.core. Best-effort:
		// failure to parse is a logged warning, not a panic.
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err == nil && p.Path != "" {
			d.cache.InvalidateByPath(p.Path)
		}
	case "EntityMaterialized", "EntitySuperseded", "SymbolDisambiguation":
		if ev.Subject != nil {
			d.cache.InvalidateByEntity(ev.Subject.ID)
		}
	}
}

// payloadHashFor extracts (content_hash, entity_id, kind) from an
// emitted event payload. Convention: every code.framework entity
// payload carries `id` and the event Kind names the entity action
// (RouteAdded → kind="Route"). Returns empty strings if the payload
// is workspace-level (no id).
func payloadHashFor(ev kernel.Event) (hash, id, kind string) {
	var p map[string]any
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return "", "", ""
	}
	idAny, ok := p["id"]
	if !ok {
		return "", "", ""
	}
	id, _ = idAny.(string)
	kindAny, ok := p["kind"]
	if ok {
		kind, _ = kindAny.(string)
	}
	// id is content-addressable in Pass 0 (MakeContentID), so the
	// id itself acts as the hash. Pass 1 may add a separate hash
	// field when entities mutate without id changes.
	return id, id, kind
}

// computeFilter builds the kernel.EventFilter that covers the union
// of every enabled extractor's Inputs(). Returns an empty filter
// (subscribe to everything) if no extractors are enabled — the loop
// will simply find no targets and ignore each event.
func (d *Dispatcher) computeFilter() kernel.EventFilter {
	layerSet := map[string]struct{}{}
	kindSet := map[string]struct{}{}
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, e := range d.entries {
		if !e.enabled.Load() {
			continue
		}
		for _, in := range e.desc.Inputs {
			lyr, knd := splitEventKind(string(in))
			if lyr != "" {
				layerSet[lyr] = struct{}{}
			}
			if knd != "" {
				kindSet[knd] = struct{}{}
			}
		}
	}
	out := kernel.EventFilter{}
	for l := range layerSet {
		out.Layers = append(out.Layers, l)
	}
	for k := range kindSet {
		out.Kinds = append(out.Kinds, k)
	}
	slices.Sort(out.Layers)
	slices.Sort(out.Kinds)
	return out
}

// rebuildIndex refreshes byInputKind from the current set of enabled
// extractors.
func (d *Dispatcher) rebuildIndex() {
	d.mu.RLock()
	defer d.mu.RUnlock()
	d.enabledMu.Lock()
	defer d.enabledMu.Unlock()
	d.byInputKind = make(map[EventKind][]*dispatchedEntry)
	for _, e := range d.entries {
		if !e.enabled.Load() || e.ext == nil {
			continue
		}
		for _, in := range e.desc.Inputs {
			d.byInputKind[in] = append(d.byInputKind[in], e)
		}
	}
}

// constructLocked instantiates an entry's Extractor. Caller MUST
// hold d.mu (write lock).
func (d *Dispatcher) constructLocked(e *dispatchedEntry) error {
	if e.ext != nil {
		return nil
	}
	ctor, _, ok := Lookup(e.desc.Name)
	if !ok {
		return fmt.Errorf("extractor %q not in registry", e.desc.Name)
	}
	deps := Deps{
		Facts:     d.cfg.Facts,
		RefCache:  d.cache,
		Workspace: d.cfg.Workspace,
		Logf:      d.cfg.Logf,
	}
	ext, err := ctor(deps)
	if err != nil {
		return fmt.Errorf("construct %s: %w", e.desc.Name, err)
	}
	e.ext = ext
	return nil
}

// Enable activates the named extractor at runtime. Persists the
// change to .graph-harness/extractors.toml (so a daemon restart
// preserves the choice). Idempotent.
func (d *Dispatcher) Enable(ctx context.Context, name string) error {
	d.mu.Lock()
	e, ok := d.entries[name]
	if !ok {
		d.mu.Unlock()
		return fmt.Errorf("unknown extractor %q", name)
	}
	if err := d.constructLocked(e); err != nil {
		d.mu.Unlock()
		return err
	}
	e.enabled.Store(true)
	d.mu.Unlock()
	d.rebuildIndex()
	if d.cfg.Workspace != "" {
		if err := WithLockedConfig(d.cfg.Workspace, func(c *Config) error {
			c.Enable(name)
			return nil
		}); err != nil {
			d.cfg.Logf("persist enable %s: %v", name, err)
		}
	}
	_ = ctx
	return nil
}

// Disable deactivates the named extractor at runtime. Persists.
// Idempotent.
func (d *Dispatcher) Disable(ctx context.Context, name string) error {
	d.mu.Lock()
	e, ok := d.entries[name]
	if !ok {
		d.mu.Unlock()
		return fmt.Errorf("unknown extractor %q", name)
	}
	e.enabled.Store(false)
	d.mu.Unlock()
	d.rebuildIndex()
	if d.cfg.Workspace != "" {
		if err := WithLockedConfig(d.cfg.Workspace, func(c *Config) error {
			c.Disable(name)
			return nil
		}); err != nil {
			d.cfg.Logf("persist disable %s: %v", name, err)
		}
	}
	_ = ctx
	return nil
}

// Status returns the live status snapshot for every registered
// extractor. Order matches Descriptors().
func (d *Dispatcher) Status() []ExtractorStatus {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]ExtractorStatus, 0, len(d.entries))
	for _, desc := range Descriptors() {
		e, ok := d.entries[desc.Name]
		if !ok {
			continue
		}
		st := ExtractorStatus{
			Name:       desc.Name,
			Family:     desc.Family,
			Languages:  desc.Languages,
			Frameworks: desc.Frameworks,
			Inputs:     desc.Inputs,
			Outputs:    desc.Outputs,
			Enabled:    e.enabled.Load(),
			LastSeq:    e.lastSeq.Load(),
			EventsIn:   e.eventsIn.Load(),
			EventsOut:  e.eventsOut.Load(),
			Errors:     e.errors.Load(),
		}
		e.errMu.Lock()
		st.LastError = e.lastError
		e.errMu.Unlock()
		out = append(out, st)
	}
	return out
}

// splitEventKind separates a qualified "<layer>.<Kind>" string into
// its two parts. Layer names use dot notation
// (`code.core`, `code.framework`, `source.live`, `semantic.overlay`)
// while kind names are CamelCase identifiers without dots, so split on
// the LAST dot.
func splitEventKind(s string) (layer, kind string) {
	i := strings.LastIndexByte(s, '.')
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+1:]
}

// deadline is a guard against pathological extractors that ignore
// ctx.Done(). The dispatcher logs and bumps the error counter if an
// extractor blocks past this deadline; the OnEvent goroutine
// continues running in the background but its result is dropped.
// Currently unused (Pass 0 OnEvent is single-shot); kept here as the
// hook for the Pass 6 perf gate.
var _ = func() time.Duration { return 5 * time.Second }
