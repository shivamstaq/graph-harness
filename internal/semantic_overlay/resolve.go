package semantic_overlay

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/dsl"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/semantic_overlay/anchors"
)

// Default resolution thresholds (SPEC §3.3 example values). Per-selector
// thresholds in the .gh `thresholds {…}` block override these.
const (
	DefaultThresholdBound      = 0.95
	DefaultThresholdReanchored = 0.75
	// AmbiguousZoneLow / High are parsed for forward compatibility; the
	// `ambiguous` outcome itself lands in P3.
	DefaultAmbiguousZoneLow  = 0.55
	DefaultAmbiguousZoneHigh = 0.75
)

// Thresholds is the per-selector resolution configuration after defaults
// are folded in.
type Thresholds struct {
	Bound      float64
	Reanchored float64
}

// AnchorTrace is one rung of the explain ladder. Populated for every anchor
// the resolver evaluates (including misses) so `--explain` can show the
// reasoning even when nothing matched.
type AnchorTrace struct {
	Index     int             `json:"index"`
	Kind      string          `json:"kind"`
	Marker    string          `json:"marker"`
	BestScore float64         `json:"best_score"`
	Threshold float64         `json:"threshold"`
	Outcome   string          `json:"outcome_when_taken"`
	Reason    string          `json:"reason"`
	Matches   []anchors.Match `json:"matches,omitempty"`
	Skipped   bool            `json:"skipped,omitempty"`
}

// Resolver walks the multi-anchor ladder for a selector. Construct one with
// NewResolver and reuse across calls; the resolver is goroutine-safe and
// caches resolved envelopes per (selector, kernel_event_seq).
type Resolver struct {
	overlay *Overlay
	store   anchors.Lookup
	cache   *resolutionCache
}

// NewResolver wires a resolver against the active overlay and store. db
// may be nil — when present it backs the SPEC §6.13 selector_resolution_cache
// table; absent, results are cached in-memory only and the kernel doesn't
// see them on restart.
func NewResolver(o *Overlay, store anchors.Lookup, db *sql.DB) (*Resolver, error) {
	c, err := newResolutionCache(db)
	if err != nil {
		return nil, err
	}
	return &Resolver{overlay: o, store: store, cache: c}, nil
}

// Resolve runs the full multi-anchor ladder for the named selector.
// Returns the SPEC §3.2 envelope. The trace channel is populated on every
// call (cache hit or miss) so callers can format `--explain` output.
func (r *Resolver) Resolve(ctx context.Context, name string, atSeq uint64) (*ResolutionEnvelope, []AnchorTrace, error) {
	sel, ok := r.overlay.Selectors[name]
	if !ok {
		return nil, nil, fmt.Errorf("selector %q not found in overlay", name)
	}
	cacheKey := selectorCacheKey(sel)
	if cached, ok := r.cache.get(cacheKey, atSeq); ok {
		return cached.envelope, cached.trace, nil
	}
	env, trace := r.evaluate(ctx, sel, atSeq)
	r.cache.put(cacheKey, atSeq, env, trace)
	return env, trace, nil
}

// evaluate is the core ladder algorithm (SPEC §3.3).
//
// Algorithm:
//
//  1. Pull any `language_id` anchors out of the ladder — they are
//     *filters*, not match producers. Their value scopes every
//     subsequent anchor's match list to entities tagged with that
//     language. Multiple `language_id` anchors are an OR over the
//     filter set. The trace records the filter as a single rung so
//     `--explain` shows which language(s) were enforced.
//  2. For each remaining anchor in declared order, evaluate. Record
//     (best score, matches) on the trace.
//  3. The first non-filter anchor (index 0 after filtering) is the
//     primary. If its best score ≥ thresh.bound → outcome=bound. Else
//     evaluate fallback anchors.
//  4. The first non-primary anchor with score ≥ thresh.reanchored →
//     outcome=reanchored.
//  5. If a primary anchor scored above reanchored but below bound and
//     no fallback fired, treat it as reanchored.
//  6. Otherwise outcome=unresolved.
//
// Cardinality: a `unique` selector with > 1 match degrades to
// outcome=unresolved with a "cardinality_violation" reason on the trace
// (full `ambiguous` outcome lands in P3 per SPEC §3.2).
func (r *Resolver) evaluate(ctx context.Context, sel *dsl.Selector, atSeq uint64) (*ResolutionEnvelope, []AnchorTrace) {
	thresh := readThresholds(sel)
	env := &ResolutionEnvelope{
		SelectorID: sel.Name,
		Outcome:    OutcomeUnresolved,
		ResolvedAt: atSeq,
	}
	trace := make([]AnchorTrace, 0, len(sel.Anchors))

	// Pull out language_id filters; the remaining anchors form the ladder.
	langFilter, ladder, langTraceIndex := splitLanguageFilter(sel.Anchors)
	if langFilter != nil {
		trace = append(trace, AnchorTrace{
			Index:  langTraceIndex,
			Kind:   "language_id",
			Marker: "anchor",
			Reason: fmt.Sprintf("filter active: language_id ∈ %v (applied to every subsequent anchor's match set)", langFilter.allowed),
		})
	}

	var (
		primaryWeak    *AnchorTrace
		primaryMatches []anchors.Match
	)
	for i, lp := range ladder {
		a := lp.anchor
		matches, err := anchors.Evaluate(ctx, a, r.store)
		matches = applyLanguageFilter(langFilter, matches)
		t := AnchorTrace{
			Index:   lp.originalIndex,
			Kind:    a.Kind,
			Marker:  a.Marker,
			Matches: matches,
		}
		bestScore := topScore(matches)
		t.BestScore = bestScore
		switch {
		case err != nil:
			t.Reason = err.Error()
			t.Skipped = true
			trace = append(trace, t)
			continue
		case len(matches) == 0:
			t.Reason = "no candidates matched"
			if langFilter != nil {
				t.Reason += fmt.Sprintf(" (after language_id filter ∈ %v)", langFilter.allowed)
			}
			t.Skipped = true
			trace = append(trace, t)
			continue
		}

		// Primary anchor (ladder index 0): bound if score ≥ thresh.Bound.
		if i == 0 {
			t.Threshold = thresh.Bound
			if bestScore >= thresh.Bound {
				if outcome, ok := applyMatches(env, sel, matches, OutcomeBound, a.Kind); ok {
					t.Outcome = string(outcome)
					t.Reason = fmt.Sprintf("primary anchor scored %.2f ≥ bound %.2f", bestScore, thresh.Bound)
					trace = append(trace, t)
					trace = append(trace, skippedRemainingLadder(ladder, i+1)...)
					return env, trace
				}
				t.Outcome = string(OutcomeUnresolved)
				t.Reason = fmt.Sprintf("primary above bound %.2f but unique-cardinality violated (%d matches)", thresh.Bound, len(matches))
				trace = append(trace, t)
				trace = append(trace, skippedRemainingLadder(ladder, i+1)...)
				env.Outcome = OutcomeUnresolved
				return env, trace
			}
			if bestScore >= thresh.Reanchored {
				captured := t
				captured.Outcome = string(OutcomeReanchored)
				captured.Reason = fmt.Sprintf("primary scored %.2f, below bound %.2f but ≥ reanchored %.2f", bestScore, thresh.Bound, thresh.Reanchored)
				primaryWeak = &captured
				primaryMatches = matches
			}
			t.Reason = fmt.Sprintf("primary scored %.2f, below bound %.2f", bestScore, thresh.Bound)
			trace = append(trace, t)
			continue
		}

		// Fallback anchors: reanchored if score ≥ thresh.Reanchored.
		t.Threshold = thresh.Reanchored
		if bestScore >= thresh.Reanchored {
			if outcome, ok := applyMatches(env, sel, matches, OutcomeReanchored, a.Kind); ok {
				t.Outcome = string(outcome)
				t.Reason = fmt.Sprintf("fallback scored %.2f ≥ reanchored %.2f", bestScore, thresh.Reanchored)
				trace = append(trace, t)
				trace = append(trace, skippedRemainingLadder(ladder, i+1)...)
				return env, trace
			}
			t.Outcome = string(OutcomeUnresolved)
			t.Reason = fmt.Sprintf("fallback above reanchored %.2f but unique-cardinality violated (%d matches)", thresh.Reanchored, len(matches))
			trace = append(trace, t)
			trace = append(trace, skippedRemainingLadder(ladder, i+1)...)
			env.Outcome = OutcomeUnresolved
			return env, trace
		}
		t.Reason = fmt.Sprintf("scored %.2f, below reanchored %.2f", bestScore, thresh.Reanchored)
		trace = append(trace, t)
	}

	// No fallback fired. If the primary scored above reanchored, settle
	// for reanchored on the primary's matches (the spec treats a weak
	// primary as a reanchor signal).
	if primaryWeak != nil && len(ladder) > 0 {
		primaryAnchor := ladder[0].anchor
		if _, ok := applyMatches(env, sel, primaryMatches, OutcomeReanchored, primaryAnchor.Kind); ok {
			// Replace the primary's recorded trace with the captured
			// reanchored disposition so --explain reflects the final pick.
			for i := range trace {
				if trace[i].Index == ladder[0].originalIndex {
					trace[i] = *primaryWeak
					break
				}
			}
		}
	}
	return env, trace
}

// ladderEntry pairs one ladder anchor with its original index in the
// authored Selector.Anchors slice. The original index is preserved so
// `--explain` output stays aligned with how the user wrote the selector
// (language_id filters keep their authored slot in the trace, and the
// remaining anchors keep their authored numbering even after the filter
// is pulled out).
type ladderEntry struct {
	anchor        *dsl.Anchor
	originalIndex int
}

// languageFilterSpec is the post-evaluation filter assembled from every
// `language_id` anchor on the selector.
type languageFilterSpec struct {
	allowed    []string
	allowedSet map[string]struct{}
}

// splitLanguageFilter walks the authored anchors, peeling out any
// `language_id` rungs into a filter spec and returning the rest as the
// resolution ladder. langTraceIndex is the originalIndex of the first
// language_id anchor encountered, used to anchor the trace entry.
func splitLanguageFilter(in []*Anchor) (*languageFilterSpec, []ladderEntry, int) {
	var (
		filter         *languageFilterSpec
		ladder         []ladderEntry
		firstFilterIdx = -1
	)
	for i, a := range in {
		if a == nil {
			continue
		}
		if a.Kind == "language_id" && a.Value != nil && a.Value.Str != nil {
			if filter == nil {
				filter = &languageFilterSpec{allowedSet: map[string]struct{}{}}
				firstFilterIdx = i
			}
			lang := normalizeLanguageAlias(*a.Value.Str)
			if lang == "" {
				continue
			}
			if _, dup := filter.allowedSet[lang]; !dup {
				filter.allowedSet[lang] = struct{}{}
				filter.allowed = append(filter.allowed, lang)
			}
			continue
		}
		ladder = append(ladder, ladderEntry{anchor: a, originalIndex: i})
	}
	return filter, ladder, firstFilterIdx
}

// applyLanguageFilter intersects matches with the language filter's
// allow-set. Nil filter passes through unchanged.
func applyLanguageFilter(f *languageFilterSpec, in []anchors.Match) []anchors.Match {
	if f == nil || len(f.allowed) == 0 {
		return in
	}
	out := in[:0:len(in)]
	for _, m := range in {
		if _, ok := f.allowedSet[normalizeLanguageAlias(m.LanguageID)]; ok {
			out = append(out, m)
		}
	}
	return out
}

// normalizeLanguageAlias mirrors anchors.LanguageID's alias table so the
// resolver's filter matches the evaluator's value semantics. Duplicated
// here to avoid reaching into the anchors package's unexported helper —
// any divergence would be a regression caught by language_id_test.go.
func normalizeLanguageAlias(id string) string {
	low := make([]byte, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		low[i] = c
	}
	switch string(low) {
	case "go", "golang":
		return "go"
	case "ts", "typescript", "javascript", "js", "tsx", "jsx":
		return "ts"
	case "py", "python":
		return "py"
	}
	return string(low)
}

// Anchor type alias to keep this file's signature compact.
type Anchor = dsl.Anchor

// skippedRemainingLadder produces a trace entry for every ladder anchor
// after the winning one. Used so --explain shows the full ladder shape
// annotated with "not consulted: earlier anchor matched".
func skippedRemainingLadder(ladder []ladderEntry, fromIndex int) []AnchorTrace {
	out := make([]AnchorTrace, 0, len(ladder)-fromIndex)
	for i := fromIndex; i < len(ladder); i++ {
		a := ladder[i].anchor
		out = append(out, AnchorTrace{
			Index:   ladder[i].originalIndex,
			Kind:    a.Kind,
			Marker:  a.Marker,
			Skipped: true,
			Reason:  "not consulted: earlier anchor satisfied threshold",
		})
	}
	return out
}

// applyMatches fills the envelope. Returns false when a unique-cardinality
// constraint is violated; the caller surfaces the violation as
// outcome=unresolved with a cardinality reason on the trace.
func applyMatches(env *ResolutionEnvelope, sel *dsl.Selector, matches []anchors.Match, outcome ResolutionOutcome, viaAnchor string) (ResolutionOutcome, bool) {
	if sel.Unique && len(matches) > 1 {
		return OutcomeUnresolved, false
	}
	env.Outcome = outcome
	env.Matches = env.Matches[:0]
	for _, m := range matches {
		env.Matches = append(env.Matches, ResolutionMatch{
			EntityID:      m.EntityID,
			QualifiedName: m.QualifiedName,
			Confidence:    m.Confidence,
			ViaAnchor:     viaAnchor,
		})
	}
	return outcome, true
}

func topScore(matches []anchors.Match) float64 {
	best := 0.0
	for _, m := range matches {
		if m.Confidence > best {
			best = m.Confidence
		}
	}
	return best
}

// readThresholds folds per-selector overrides into the SPEC §3.3 defaults.
func readThresholds(sel *dsl.Selector) Thresholds {
	t := Thresholds{Bound: DefaultThresholdBound, Reanchored: DefaultThresholdReanchored}
	if sel.Thresholds == nil {
		return t
	}
	for _, item := range sel.Thresholds.Items {
		val := numberValue(item)
		switch item.Name {
		case "bound":
			if val > 0 {
				t.Bound = val
			}
		case "reanchored":
			if val > 0 {
				t.Reanchored = val
			}
		}
	}
	return t
}

func numberValue(it *dsl.ThresholdItem) float64 {
	switch {
	case it.Float != nil:
		return *it.Float
	case it.Int != nil:
		return float64(*it.Int)
	}
	return 0
}

// selectorCacheKey hashes the selector AST so cache hits compose with
// authoring edits — any change to anchors / thresholds / cardinality
// produces a new key so we never serve stale resolutions.
func selectorCacheKey(sel *dsl.Selector) string {
	canonical, _ := json.Marshal(sel)
	h := sha256.Sum256(canonical)
	return hex.EncodeToString(h[:])
}

// resolutionCache is the SPEC §6.13 cache: per-selector content-keyed
// envelopes plus their trace, indexed at the kernel sequence they
// resolved at. Drift events on the code.core layer invalidate the cache;
// the kernel emits invalidations via WireDriftInvalidation.
type resolutionCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	db      *sql.DB
}

type cacheEntry struct {
	resolvedAt uint64
	envelope   *ResolutionEnvelope
	trace      []AnchorTrace
}

func newResolutionCache(db *sql.DB) (*resolutionCache, error) {
	c := &resolutionCache{entries: map[string]cacheEntry{}, db: db}
	if db == nil {
		return c, nil
	}
	const schema = `
CREATE TABLE IF NOT EXISTS selector_resolution_cache (
    selector_key TEXT PRIMARY KEY,
    resolved_at  INTEGER NOT NULL,
    envelope     BLOB    NOT NULL,
    trace        BLOB    NOT NULL
);
`
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *resolutionCache) get(key string, atSeq uint64) (cacheEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok || e.resolvedAt != atSeq {
		return cacheEntry{}, false
	}
	return e, true
}

func (c *resolutionCache) put(key string, atSeq uint64, env *ResolutionEnvelope, trace []AnchorTrace) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{resolvedAt: atSeq, envelope: env, trace: trace}
	if c.db == nil {
		return
	}
	envJSON, _ := json.Marshal(env)
	traceJSON, _ := json.Marshal(trace)
	_, _ = c.db.Exec(`
		INSERT INTO selector_resolution_cache (selector_key, resolved_at, envelope, trace)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(selector_key) DO UPDATE SET
		    resolved_at = excluded.resolved_at,
		    envelope    = excluded.envelope,
		    trace       = excluded.trace
	`, key, atSeq, envJSON, traceJSON)
}

// PriorAt returns the most recent cache entry for selectorName whose
// resolved_at is strictly less than seq. Used by change.process to detect
// unresolved_anchor findings: a selector that was bound at seq-1 but is
// unresolved at seq fires the finding (SPEC §8.2 + plan §P1.T30).
//
// The lookup is keyed on the selector's *current* AST hash, so an
// unrelated authoring edit between runs intentionally invalidates the
// prior entry — the comparison only fires when the same selector
// definition transitions outcomes due to code-side drift.
func (r *Resolver) PriorAt(ctx context.Context, name string, seq uint64) (*ResolutionEnvelope, bool, error) {
	sel, ok := r.overlay.Selectors[name]
	if !ok {
		return nil, false, fmt.Errorf("selector %q not found in overlay", name)
	}
	key := selectorCacheKey(sel)
	return r.cache.priorAt(ctx, key, seq)
}

func (c *resolutionCache) priorAt(_ context.Context, key string, seq uint64) (*ResolutionEnvelope, bool, error) {
	c.mu.RLock()
	if e, ok := c.entries[key]; ok && e.resolvedAt < seq {
		c.mu.RUnlock()
		return e.envelope, true, nil
	}
	c.mu.RUnlock()
	if c.db == nil {
		return nil, false, nil
	}
	row := c.db.QueryRow(
		`SELECT envelope FROM selector_resolution_cache
		 WHERE selector_key = ? AND resolved_at < ?
		 ORDER BY resolved_at DESC LIMIT 1`, key, seq)
	var envJSON []byte
	if err := row.Scan(&envJSON); err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}
	var env ResolutionEnvelope
	if err := json.Unmarshal(envJSON, &env); err != nil {
		return nil, false, err
	}
	return &env, true, nil
}

// invalidate drops every cached envelope. Called on subscribed drift events.
func (c *resolutionCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]cacheEntry{}
	if c.db != nil {
		_, _ = c.db.Exec(`DELETE FROM selector_resolution_cache`)
	}
}

// driftEventKinds is the SPEC §3.4 invalidation-trigger set published by
// the code.core layer. The resolver clears its cache on any of these.
var driftEventKinds = []string{
	"SymbolMoved",
	"SymbolRenamed",
	"SignatureChanged",
	"SymbolDeleted",
}

// WireDriftInvalidation subscribes the resolver's cache to drift events on
// the code.core layer (SPEC §3.4) and clears the cache on each match.
// Returns a stop function that closes the subscription.
//
// Long-running callers (daemons) keep the stop closure for the resolver's
// lifetime; one-shot CLIs construct a resolver, run a single Resolve, and
// let the function return without subscribing. Subscriptions are cheap —
// they never block the producer.
func (r *Resolver) WireDriftInvalidation(log *facts.EventLog) (stop func()) {
	stream := log.SubscribeWithFilter(kernel.EventFilter{
		Layers: []string{"code.core"},
		Kinds:  driftEventKinds,
	})
	done := make(chan struct{})
	go func() {
		for {
			select {
			case _, ok := <-stream.Events():
				if !ok {
					return
				}
				r.cache.invalidate()
			case <-done:
				_ = stream.Close()
				return
			}
		}
	}()
	return func() { close(done) }
}

// CardinalityViolation reports whether the most recent ladder run hit a
// unique-cardinality > 1 case. Surfaced by change.process as a low-priority
// finding (full `ambiguous` outcome lands in P3).
func (e *AnchorTrace) CardinalityViolation() bool {
	return e.Outcome == string(OutcomeUnresolved) &&
		(stringContains(e.Reason, "unique-cardinality"))
}

func stringContains(s, sub string) bool {
	return sub != "" && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	// O(n*m) substring; sub is constant-length and short.
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// SortedTrace returns trace entries sorted by anchor index ascending.
// Convenient for stable --explain output.
func SortedTrace(in []AnchorTrace) []AnchorTrace {
	out := append([]AnchorTrace(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// EntityResolverFromStore adapts a *code_core.Store to the anchors.Lookup
// interface. Centralized here so callers (resolver, --explain CLI, future
// MCP adapter) all wire the same surface.
func EntityResolverFromStore(s *code_core.Store) anchors.Lookup { return s }
