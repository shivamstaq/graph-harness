package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Router is the kernel's single public read seam above Facts adapters
// (SPEC §7.4). Every consumer-facing read method — `selectors test`,
// `entity provenance`, `code list`, `flows list`, `query`, the MCP
// surfaces, the validation pipeline's snapshot reads — funnels through
// Router.Route. The router parses the query, dispatches to one or more
// layer adapters via the LayerReader contract, folds provenance per
// §4.5, and returns a single ResultEnvelope with a stable
// resolved_at_seq.
//
// Direct r.Code.… / r.Overlay.… calls in CLI handlers or surface
// adapters are forbidden in steady state — they exist only as the P0
// shortcut that P0.5.T13 closes.
type Router struct {
	mu     sync.RWMutex
	layers map[string]LayerReader
	head   HeadSeq
}

// LayerReader is the per-layer read contract the router fans out
// against. Each layer's Facts adapter implements it; for P0.5 the
// code.core and semantic.overlay adapters are bound directly to their
// Store / Overlay types via thin wrappers in this package's callers.
//
// The router treats LayerReader as opaque: it does not know what
// capabilities each layer declares — the layer's own ReadCurrent /
// ReadAsOf path enforces capability gating and returns
// ErrCapabilityNotDeclared (or equivalent) when a query falls outside
// the declared surface.
type LayerReader interface {
	ReadCurrent(ctx context.Context, q LayerQuery) (ResultEnvelope, error)
	ReadAsOf(ctx context.Context, seq uint64, q LayerQuery) (ResultEnvelope, error)
}

// HeadSeq reports the current kernel head. The router pins every read
// it serves to a single head seq sampled at Route() entry, so a
// multi-layer fan-out cannot straddle a commit boundary.
type HeadSeq func() uint64

// NewRouter returns an empty router. Register layers via Register
// before serving traffic.
func NewRouter(head HeadSeq) *Router {
	if head == nil {
		head = func() uint64 { return 0 }
	}
	return &Router{
		layers: map[string]LayerReader{},
		head:   head,
	}
}

// Register binds an adapter to a layer name. Re-registering replaces
// the prior adapter atomically.
func (r *Router) Register(layer string, reader LayerReader) {
	r.mu.Lock()
	r.layers[layer] = reader
	r.mu.Unlock()
}

// Layers returns the sorted list of registered layer names.
func (r *Router) Layers() []string {
	r.mu.RLock()
	out := make([]string, 0, len(r.layers))
	for n := range r.layers {
		out = append(out, n)
	}
	r.mu.RUnlock()
	sort.Strings(out)
	return out
}

// HeadAt returns the current pinned-head seq the router sees. Used
// by callers that need to surface "resolved_at_kernel_seq" without
// going through a full Route call.
func (r *Router) HeadAt() uint64 { return r.head() }

// RouteRequest is the typed v1 query surface. Exactly one of the
// envelope fields must be set; the router rejects ambiguous requests
// with ErrMalformedQuery so consumers can't accidentally fan out into
// multiple decompositions in one call.
//
// SPEC §7.4 lists the v1 grammar as Participle-parsed DSL strings; the
// P0.5 implementation accepts the lowered struct form callers
// (CLI/JSON-RPC) build by hand. The DSL parser front-end is wired
// alongside the broader query.parse method; it constructs the same
// RouteRequest envelope from the parsed AST.
type RouteRequest struct {
	// Layer is the target layer name. Required.
	Layer string `json:"layer"`
	// Capability is the layer-declared capability the query exercises.
	// The router does not interpret the value — it is forwarded
	// verbatim to the layer's ReadCurrent so the adapter can do its
	// own gating + dispatch.
	Capability string `json:"capability"`
	// Args is the capability-specific argument blob (opaque to the
	// router; the layer adapter unmarshals).
	Args json.RawMessage `json:"args,omitempty"`
	// AsOfSeq, when non-zero, pins the read to a historical seq via
	// Facts.ReadAsOf instead of ReadCurrent. The router stamps the
	// envelope's resolved_at_seq with this value verbatim.
	AsOfSeq uint64 `json:"as_of_seq,omitempty"`
}

// ErrMalformedQuery is returned when RouteRequest fails validation.
type RouterError string

// Error implements error.
func (e RouterError) Error() string { return string(e) }

// Sentinel router errors.
const (
	ErrMalformedQuery RouterError = "router: malformed query (layer + capability required)"
	ErrLayerNotFound  RouterError = "router: target layer not registered"
)

// Route is the single public read seam: parse → decompose → fan out →
// fold → envelope.
//
// For P0.5 the "parse" step is the caller's responsibility — they
// build a RouteRequest envelope directly. Decomposition is one-layer-
// one-call in this iteration; multi-layer fan-out (e.g. queries that
// traverse from code.core into semantic.overlay) lands in P3 when
// Mangle rule packs become the cross-layer composition mechanism.
//
// The returned ResultEnvelope carries:
//   - Data: the layer adapter's verbatim response (opaque to the router)
//   - ResolvedAtSeq: the seq the read pinned to (head at entry, or
//     AsOfSeq when supplied)
//   - Provenance: the layer adapter's per-source claim, folded via
//     FoldProvenance when the layer returned multiple constituents
//   - NotReproducible: forwarded from the adapter (set when the layer
//     could not pin its read to a stable snapshot — e.g. live LSP
//     queries against an editor buffer)
func (r *Router) Route(ctx context.Context, req RouteRequest) (ResultEnvelope, error) {
	if req.Layer == "" || req.Capability == "" {
		return ResultEnvelope{}, ErrMalformedQuery
	}
	r.mu.RLock()
	reader, ok := r.layers[req.Layer]
	r.mu.RUnlock()
	if !ok {
		return ResultEnvelope{}, fmt.Errorf("%w: %s", ErrLayerNotFound, req.Layer)
	}
	q := LayerQuery{Capability: req.Capability, Args: req.Args}
	if req.AsOfSeq > 0 {
		env, err := reader.ReadAsOf(ctx, req.AsOfSeq, q)
		if err != nil {
			return ResultEnvelope{}, err
		}
		if env.ResolvedAtSeq == 0 {
			env.ResolvedAtSeq = req.AsOfSeq
		}
		return env, nil
	}
	headSeq := r.head()
	env, err := reader.ReadCurrent(ctx, q)
	if err != nil {
		return ResultEnvelope{}, err
	}
	if env.ResolvedAtSeq == 0 {
		env.ResolvedAtSeq = headSeq
	}
	return env, nil
}

// Compose folds a slice of per-layer envelopes into a single
// ResultEnvelope. Used by future multi-layer queries (P3+); exposed
// in P0.5 so callers that already do their own multi-source merge
// (validate-diff, harness.explore) can lower to the same fold once
// they migrate.
//
// The combined envelope carries:
//   - Data: a JSON object keyed by layer name with each constituent's Data
//   - ResolvedAtSeq: the max of constituents (kernel-head invariant: every
//     constituent pins to the same head at entry, so max == any)
//   - Provenance: FoldProvenance over constituents (SPEC §4.5)
//   - NotReproducible: OR over constituents
func Compose(envs map[string]ResultEnvelope) ResultEnvelope {
	if len(envs) == 0 {
		return ResultEnvelope{}
	}
	keys := make([]string, 0, len(envs))
	for k := range envs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	provs := make([]Provenance, 0, len(envs))
	data := map[string]json.RawMessage{}
	var maxSeq uint64
	var notRepro bool
	for _, k := range keys {
		env := envs[k]
		data[k] = env.Data
		provs = append(provs, env.Provenance)
		if env.ResolvedAtSeq > maxSeq {
			maxSeq = env.ResolvedAtSeq
		}
		notRepro = notRepro || env.NotReproducible
	}
	out := ResultEnvelope{
		ResolvedAtSeq:   maxSeq,
		Provenance:      FoldProvenance(provs),
		NotReproducible: notRepro,
	}
	if blob, err := json.Marshal(data); err == nil {
		out.Data = blob
	}
	return out
}
