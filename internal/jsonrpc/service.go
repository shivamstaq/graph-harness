package jsonrpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shivamstaq/graph-harness/internal/change_process"
	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/dsl"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/review_queue"
	"github.com/shivamstaq/graph-harness/internal/semantic_overlay"
	"github.com/shivamstaq/graph-harness/internal/source_live/detect"
)

// Service is the application surface the JSON-RPC server dispatches into.
// It owns the long-lived workspace handles (event log, code.core store,
// review queue) that all RPC methods need. One Service per daemon process.
//
// Service is safe for concurrent use; RPC methods take a snapshot of the
// overlay (lock-protected) at call time and do not mutate the kernel
// state outside of the explicit write methods (overlay.save, review.*).
type Service struct {
	Workspace *daemon.Workspace
	Log       *facts.EventLog
	Code      *code_core.Store
	Queue     *review_queue.Queue
	Registry  *kernel.Registry

	mu      sync.RWMutex
	overlay *semantic_overlay.Overlay

	// Conflicts is a ring buffer of recent SymbolDisambiguation events.
	// Surfaces (TUI, Studio) read it via conflicts.list. The buffer is
	// populated by the three-source unifier (T-core's task #5); until
	// that lands the surface returns an empty list which is the correct
	// observable behavior.
	confMu    sync.Mutex
	conflicts []ConflictRecord

	// shutdown is closed when daemon.shutdown is invoked. The daemon
	// reads it from the listener loop to begin orderly close.
	shutdownOnce sync.Once
	shutdown     chan struct{}

	// activity tracks the last RPC time so the daemon's idle timeout
	// loop can decide when to exit. Wall-clock monotonic.
	activityMu sync.Mutex
	lastActive time.Time

	// subs is the SPEC §6.22 long-lived subscriber bookkeeping. One
	// per Service; lazily constructed on first access to keep tests
	// that pass nil Log paths working.
	subsOnce sync.Once
	subs     *SubscriptionManager

	// trans is the SPEC §6.19 in-memory transient overlay tier.
	// Same lazy-init pattern as subs.
	transOnce sync.Once
	trans     *TransientOverlay

	// router is the SPEC §7.4 / P0.5.T13 Kernel.Route read seam.
	// All consumer-facing read methods on Service lower through it
	// so direct Code.* / Overlay.* calls in handler bodies stay at
	// zero in steady state.
	routerOnce sync.Once
	router     *kernel.Router

	// extractors is the code.framework Dispatcher injected after
	// construction via SetExtractors. nil-safe: extractors.list /
	// status return descriptors from the package-level registry when
	// the daemon hasn't wired a Dispatcher (one-shot CLI batch path).
	extractorsMu sync.RWMutex
	extractors   *code_framework.Dispatcher
}

// SetExtractors installs the code.framework Dispatcher on this
// Service. Called by the daemon's bootstrap after Workspace.Open and
// before the listener goroutine starts. Idempotent.
func (s *Service) SetExtractors(d *code_framework.Dispatcher) {
	s.extractorsMu.Lock()
	s.extractors = d
	s.extractorsMu.Unlock()
}

// Extractors returns the installed Dispatcher (may be nil).
func (s *Service) Extractors() *code_framework.Dispatcher {
	s.extractorsMu.RLock()
	defer s.extractorsMu.RUnlock()
	return s.extractors
}

// Router returns the Service's Kernel.Route surface, lazily installing
// the layer adapters on first use. CLI/JSON-RPC handlers funnel reads
// through this router instead of poking at s.Code / s.Overlay()
// directly (SPEC §7.4).
func (s *Service) Router() *kernel.Router {
	s.routerOnce.Do(func() { s.router = installRouter(s) })
	return s.router
}

// subscriptions returns the lazily-constructed SubscriptionManager.
// Each access after the first returns the same instance.
func (s *Service) subscriptions() *SubscriptionManager {
	s.subsOnce.Do(func() {
		s.subs = NewSubscriptionManager(s.Log)
	})
	return s.subs
}

// StartSubscriptionEviction kicks off the SPEC §6.22 heartbeat
// eviction sweep on the Service's SubscriptionManager. Called from
// daemon serve at boot (P1.5.T10) so silent subscribers past the
// idle threshold are dropped continuously in production rather than
// only when a test invokes the sweep manually. Passing tickInterval=0
// uses the manager's default (idleThreshold/4, clamped at 1s).
func (s *Service) StartSubscriptionEviction(ctx context.Context) {
	s.subscriptions().StartEvictionLoop(ctx, 0)
}

// SetSubscriptionIdleThreshold overrides the SPEC §6.22 idle
// threshold on the underlying SubscriptionManager. F17 — the
// `daemon serve --eviction-threshold <duration>` flag plumbs the
// per-process override here so e2e specs can drive observable
// evictions in CI-friendly time.
func (s *Service) SetSubscriptionIdleThreshold(d time.Duration) {
	s.subscriptions().SetIdleThreshold(d)
}

// transient returns the lazily-constructed TransientOverlay. The
// overlay's overflow sink is wired to emit a kernel.transient
// TransientTierOverflow event on the kernel bus so subscribers can
// learn about evictions (SPEC §6.19 + plan §3 gate 10/11). Best-
// effort: emission failure is logged but does not stop the eviction.
func (s *Service) transient() *TransientOverlay {
	s.transOnce.Do(func() {
		s.trans = NewTransientOverlay()
		if s.Log != nil {
			s.trans.SetOverflowSink(func(ev TransientEntry) {
				payload, _ := json.Marshal(map[string]any{
					"subscriber_id": ev.SubscriberID,
					"source_class":  ev.SourceClass,
					"target_id":     ev.TargetID,
					"reason":        "lru_eviction",
				})
				_, _ = s.Log.Append(context.Background(), []kernel.Event{{
					Layer:      "kernel.transient",
					Kind:       "TransientTierOverflow",
					Payload:    payload,
					ProducedBy: kernel.SourceLayerInternal,
				}})
			})
		}
	})
	return s.trans
}

// ConflictRecord is one SymbolDisambiguation event surfaced over RPC.
type ConflictRecord struct {
	Seq        uint64    `json:"seq"`
	Selector   string    `json:"selector"`
	Qualified  string    `json:"qualified_name"`
	Sources    []string  `json:"sources"`
	DetectedAt time.Time `json:"detected_at"`
}

// NewService constructs a Service from already-opened resources.
func NewService(
	ws *daemon.Workspace,
	log *facts.EventLog,
	code *code_core.Store,
	queue *review_queue.Queue,
	reg *kernel.Registry,
	overlay *semantic_overlay.Overlay,
) *Service {
	return &Service{
		Workspace:  ws,
		Log:        log,
		Code:       code,
		Queue:      queue,
		Registry:   reg,
		overlay:    overlay,
		shutdown:   make(chan struct{}),
		lastActive: time.Now(),
	}
}

// Touch records activity for idle-timeout accounting.
func (s *Service) Touch() {
	s.activityMu.Lock()
	s.lastActive = time.Now()
	s.activityMu.Unlock()
}

// LastActive returns the wall-clock time of the most recent RPC.
func (s *Service) LastActive() time.Time {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	return s.lastActive
}

// ShutdownC returns a channel that is closed when daemon.shutdown is invoked.
func (s *Service) ShutdownC() <-chan struct{} { return s.shutdown }

// Shutdown signals the listener loop to exit.
func (s *Service) Shutdown() {
	s.shutdownOnce.Do(func() { close(s.shutdown) })
}

// Overlay returns the current snapshot of the in-memory overlay (read-locked).
func (s *Service) Overlay() *semantic_overlay.Overlay {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.overlay
}

// ReplaceOverlay swaps in a freshly loaded overlay (used after overlay.save).
func (s *Service) ReplaceOverlay(o *semantic_overlay.Overlay) {
	s.mu.Lock()
	s.overlay = o
	s.mu.Unlock()
}

// RecordConflict appends a SymbolDisambiguation event to the conflict ring
// buffer. Bounded to the last 256 events to keep memory predictable.
func (s *Service) RecordConflict(c ConflictRecord) {
	s.confMu.Lock()
	defer s.confMu.Unlock()
	s.conflicts = append(s.conflicts, c)
	if len(s.conflicts) > 256 {
		s.conflicts = s.conflicts[len(s.conflicts)-256:]
	}
}

// Conflicts returns the current conflict ring buffer (newest last).
func (s *Service) Conflicts() []ConflictRecord {
	s.confMu.Lock()
	defer s.confMu.Unlock()
	out := make([]ConflictRecord, len(s.conflicts))
	copy(out, s.conflicts)
	return out
}

// --- Method handlers ---------------------------------------------------------

// PingResult is the daemon.ping reply.
type PingResult struct {
	Pong    bool   `json:"pong"`
	Version string `json:"version"`
}

// Ping handles daemon.ping.
func (s *Service) Ping(_ context.Context) (PingResult, error) {
	return PingResult{Pong: true, Version: "0.1.0-dev"}, nil
}

// StatusResult is the status method reply.
type StatusResult struct {
	WorkspaceRoot string `json:"workspace_root"`
	WorkspaceID   string `json:"workspace_id"`
	SocketPath    string `json:"socket_path"`
	EventLogPath  string `json:"event_log_path"`
	LastSeq       uint64 `json:"last_seq"`
	OverlayCount  int    `json:"overlay_count"`
}

// Status handles status.
func (s *Service) Status(_ context.Context) (StatusResult, error) {
	o := s.Overlay()
	return StatusResult{
		WorkspaceRoot: s.Workspace.Root,
		WorkspaceID:   s.Workspace.ID,
		SocketPath:    s.Workspace.SocketPath,
		EventLogPath:  s.Workspace.EventLog,
		LastSeq:       s.Log.LastSeq(),
		OverlayCount:  len(o.Selectors) + len(o.Flows),
	}, nil
}

// LayersResult is the layers.list reply.
type LayersResult struct {
	Layers []LayerInfo `json:"layers"`
}

// LayerInfo describes one installed layer.
type LayerInfo struct {
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	DependsOn []string `json:"depends_on"`
}

// LayersList handles layers.list.
func (s *Service) LayersList(_ context.Context) (LayersResult, error) {
	if s.Registry == nil {
		return LayersResult{Layers: []LayerInfo{}}, nil
	}
	names := s.Registry.List()
	out := make([]LayerInfo, 0, len(names))
	for _, n := range names {
		m, ok := s.Registry.Get(n)
		if !ok {
			continue
		}
		out = append(out, LayerInfo{
			Name:      m.Metadata.Name,
			Version:   m.Metadata.Version,
			DependsOn: append([]string(nil), m.Metadata.DependsOn...),
		})
	}
	return LayersResult{Layers: out}, nil
}

// SelectorsTestParams names the selector to resolve.
type SelectorsTestParams struct {
	Name string `json:"name"`
}

// SelectorsTest handles selectors.test. Lowers through Kernel.Route
// rather than calling Overlay().Resolve directly (SPEC §7.4 / P0.5.T13).
func (s *Service) SelectorsTest(ctx context.Context, p SelectorsTestParams) (*semantic_overlay.ResolutionEnvelope, error) {
	if p.Name == "" {
		return nil, errors.New("selector name required")
	}
	args, err := json.Marshal(overlayResolveArgs{Name: p.Name})
	if err != nil {
		return nil, err
	}
	env, err := s.Router().Route(ctx, kernel.RouteRequest{
		Layer:      "semantic.overlay",
		Capability: CapOverlayResolveSelector,
		Args:       args,
	})
	if err != nil {
		return nil, err
	}
	var out semantic_overlay.ResolutionEnvelope
	if err := json.Unmarshal(env.Data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SelectorsPreviewParams supplies a literal anchor + kind for one-shot
// resolution (used by Studio's live-preview pane and VS Code code lens
// before a selector is committed to disk).
type SelectorsPreviewParams struct {
	Kind  string `json:"kind"`  // qualified_name | symbol_fingerprint | path_glob | body_hash
	Value string `json:"value"` // anchor literal
}

// SelectorsPreviewResult is the lightweight preview envelope.
type SelectorsPreviewResult struct {
	Outcome  string                             `json:"outcome"`
	Matches  []semantic_overlay.ResolutionMatch `json:"matches"`
	Resolved uint64                             `json:"resolved_at_kernel_seq"`
}

// SelectorsPreview handles selectors.preview. Routes the qualified_name
// path through Kernel.Route's code.core/lookup_by_qualified_name
// capability. The qualified_name_suffix branch still uses the direct
// adapter call: suffix lookups have no router capability declared (the
// router exposes the exact-match capability only) and adding a suffix
// capability is deferred to P3 alongside the broader Mangle rule pack.
func (s *Service) SelectorsPreview(ctx context.Context, p SelectorsPreviewParams) (SelectorsPreviewResult, error) {
	res := SelectorsPreviewResult{Outcome: "unresolved", Matches: []semantic_overlay.ResolutionMatch{}, Resolved: s.Log.LastSeq()}
	switch p.Kind {
	case "qualified_name":
		args, err := json.Marshal(codeLookupEntityArgs{QualifiedName: p.Value})
		if err != nil {
			return res, err
		}
		env, err := s.Router().Route(ctx, kernel.RouteRequest{
			Layer:      "code.core",
			Capability: CapCodeLookupByQName,
			Args:       args,
		})
		if err != nil {
			return res, err
		}
		res.Resolved = env.ResolvedAtSeq
		if string(env.Data) == "null" {
			return res, nil
		}
		var ent code_core.Entity
		if err := json.Unmarshal(env.Data, &ent); err != nil {
			return res, err
		}
		res.Outcome = "bound"
		res.Matches = append(res.Matches, semantic_overlay.ResolutionMatch{
			EntityID:      ent.ID,
			QualifiedName: ent.QualifiedName,
			Confidence:    1.0,
			ViaAnchor:     "qualified_name",
		})
	case "qualified_name_suffix":
		ent, err := s.Code.LookupByQualifiedNameSuffix(ctx, p.Value)
		if err != nil {
			return res, err
		}
		if ent != nil {
			res.Outcome = "bound"
			res.Matches = append(res.Matches, semantic_overlay.ResolutionMatch{
				EntityID:      ent.ID,
				QualifiedName: ent.QualifiedName,
				Confidence:    0.85,
				ViaAnchor:     "qualified_name_suffix",
			})
		}
	default:
		return res, fmt.Errorf("unsupported anchor kind %q (preview supports qualified_name, qualified_name_suffix)", p.Kind)
	}
	return res, nil
}

// FlowsListResult mirrors the CLI flows list response.
type FlowsListResult struct {
	Flows []FlowSummary `json:"flows"`
}

// FlowSummary is the per-flow projection.
type FlowSummary struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Scope       string `json:"scope,omitempty"`
	Steps       int    `json:"steps"`
}

// FlowsList handles flows.list. Lowers through Kernel.Route.
func (s *Service) FlowsList(ctx context.Context) (FlowsListResult, error) {
	env, err := s.Router().Route(ctx, kernel.RouteRequest{
		Layer:      "semantic.overlay",
		Capability: CapOverlayFlowsList,
	})
	if err != nil {
		return FlowsListResult{}, err
	}
	var rows []FlowSummary
	if err := json.Unmarshal(env.Data, &rows); err != nil {
		return FlowsListResult{}, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	if rows == nil {
		rows = []FlowSummary{}
	}
	return FlowsListResult{Flows: rows}, nil
}

// QueryParseParams carries the DSL source.
type QueryParseParams struct {
	Source string `json:"source"`
}

// QueryParseResult is the AST envelope.
type QueryParseResult struct {
	ResolvedAtKernelSeq uint64 `json:"resolved_at_kernel_seq"`
	DeclarationCount    int    `json:"declarations"`
	CanonicalGH         string `json:"canonical_gh"`
}

// QueryParse handles query.parse.
func (s *Service) QueryParse(_ context.Context, p QueryParseParams) (QueryParseResult, error) {
	if strings.TrimSpace(p.Source) == "" {
		return QueryParseResult{}, errors.New("source required")
	}
	f, err := dsl.ParseString("inline.gh", p.Source)
	if err != nil {
		return QueryParseResult{}, err
	}
	return QueryParseResult{
		ResolvedAtKernelSeq: s.Log.LastSeq(),
		DeclarationCount:    len(f.Decls),
		CanonicalGH:         dsl.Render(f),
	}, nil
}

// ValidateDiffParams is the validate.diff input.
type ValidateDiffParams struct {
	Diff string `json:"diff"`
}

// ValidateDiff handles validate.diff.
func (s *Service) ValidateDiff(ctx context.Context, p ValidateDiffParams) (*change_process.ValidateDiffResult, error) {
	if strings.TrimSpace(p.Diff) == "" {
		return nil, errors.New("diff required")
	}
	pipe := &change_process.Pipeline{Overlay: s.Overlay(), Code: s.Code}
	return pipe.ValidateDiff(ctx, []byte(p.Diff), s.Log.LastSeq())
}

// ReviewListParams allows filtering by state.
type ReviewListParams struct {
	State string `json:"state,omitempty"`
}

// ReviewListResult is the wrapper.
type ReviewListResult struct {
	Items []review_queue.Proposal `json:"items"`
}

// ReviewList handles review.list.
func (s *Service) ReviewList(ctx context.Context, p ReviewListParams) (ReviewListResult, error) {
	items, err := s.Queue.List(ctx, review_queue.State(p.State))
	if err != nil {
		return ReviewListResult{}, err
	}
	return ReviewListResult{Items: items}, nil
}

// ReviewIDParams identifies one proposal.
type ReviewIDParams struct {
	ID string `json:"id"`
}

// ReviewGet handles review.get.
func (s *Service) ReviewGet(ctx context.Context, p ReviewIDParams) (*review_queue.Proposal, error) {
	if p.ID == "" {
		return nil, errors.New("id required")
	}
	return s.Queue.Get(ctx, p.ID)
}

// ReviewAck is the simple ack reply for review.accept / review.reject.
type ReviewAck struct {
	OK bool `json:"ok"`
}

// ReviewAccept handles review.accept.
func (s *Service) ReviewAccept(ctx context.Context, p ReviewIDParams) (ReviewAck, error) {
	if p.ID == "" {
		return ReviewAck{}, errors.New("id required")
	}
	if err := s.Queue.Accept(ctx, p.ID); err != nil {
		return ReviewAck{}, err
	}
	return ReviewAck{OK: true}, nil
}

// ReviewReject handles review.reject.
func (s *Service) ReviewReject(ctx context.Context, p ReviewIDParams) (ReviewAck, error) {
	if p.ID == "" {
		return ReviewAck{}, errors.New("id required")
	}
	if err := s.Queue.Reject(ctx, p.ID); err != nil {
		return ReviewAck{}, err
	}
	return ReviewAck{OK: true}, nil
}

// ReviewSubmitParams is the input shape for review.submit (P1.5.T06).
type ReviewSubmitParams struct {
	TargetLayer string                    `json:"target_layer"`
	Kind        string                    `json:"kind"`
	Author      string                    `json:"author"`
	Description string                    `json:"description,omitempty"`
	Payload     json.RawMessage           `json:"payload,omitempty"`
	Evidence    []review_queue.EvidenceItem `json:"evidence,omitempty"`
}

// ReviewSubmitResult carries the newly-allocated proposal id + the
// proposal's resolved state (needs_evidence when requirements unmet).
type ReviewSubmitResult struct {
	ID    string                  `json:"id"`
	State string                  `json:"state"`
	Item  *review_queue.Proposal  `json:"item,omitempty"`
}

// ReviewSubmit handles review.submit — the daemon-canonical write
// path for proposals. P1.5.T06.
func (s *Service) ReviewSubmit(ctx context.Context, p ReviewSubmitParams) (ReviewSubmitResult, error) {
	id, err := s.Queue.Submit(ctx, review_queue.Proposal{
		TargetLayer: p.TargetLayer,
		Kind:        p.Kind,
		Author:      p.Author,
		Description: p.Description,
		Payload:     []byte(p.Payload),
		Evidence:    p.Evidence,
	})
	if err != nil {
		return ReviewSubmitResult{}, err
	}
	item, err := s.Queue.Get(ctx, id)
	if err != nil {
		return ReviewSubmitResult{ID: id}, err
	}
	state := ""
	if item != nil {
		state = string(item.State)
	}
	return ReviewSubmitResult{ID: id, State: state, Item: item}, nil
}

// ReviewAddEvidenceParams is the input for review.addEvidence.
type ReviewAddEvidenceParams struct {
	ID       string                       `json:"id"`
	Evidence []review_queue.EvidenceItem  `json:"evidence"`
}

// ReviewAddEvidenceResult reports the proposal's state after the
// evidence was appended (may auto-promote to pending_review).
type ReviewAddEvidenceResult struct {
	State string `json:"state"`
}

// ReviewAddEvidence handles review.addEvidence — daemon-canonical
// write path for attaching evidence to a needs_evidence proposal.
// P1.5.T06.
func (s *Service) ReviewAddEvidence(ctx context.Context, p ReviewAddEvidenceParams) (ReviewAddEvidenceResult, error) {
	if p.ID == "" {
		return ReviewAddEvidenceResult{}, errors.New("id required")
	}
	st, err := s.Queue.AddEvidence(ctx, p.ID, p.Evidence)
	if err != nil {
		return ReviewAddEvidenceResult{}, err
	}
	return ReviewAddEvidenceResult{State: string(st)}, nil
}

// OverlaySaveParams writes a .gh file under .graph-harness/overlay/.
// The relative path is sanitized: no `..` segments, no absolute paths.
type OverlaySaveParams struct {
	RelPath string `json:"rel_path"` // e.g. "checkout.gh"
	Source  string `json:"source"`
}

// OverlaySaveResult reports the canonical absolute path written.
type OverlaySaveResult struct {
	Path         string `json:"path"`
	BytesWritten int    `json:"bytes_written"`
	SHA256       string `json:"sha256"`
}

// OverlaySave handles overlay.save.
func (s *Service) OverlaySave(_ context.Context, p OverlaySaveParams) (OverlaySaveResult, error) {
	if p.RelPath == "" {
		return OverlaySaveResult{}, errors.New("rel_path required")
	}
	if !strings.HasSuffix(p.RelPath, ".gh") {
		return OverlaySaveResult{}, errors.New("rel_path must end in .gh")
	}
	clean := filepath.Clean(p.RelPath)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || strings.Contains(clean, string(filepath.Separator)+"..") {
		return OverlaySaveResult{}, fmt.Errorf("rel_path %q escapes overlay dir", p.RelPath)
	}
	// Round-trip parse to reject invalid .gh before writing.
	if _, err := dsl.ParseString(clean, p.Source); err != nil {
		return OverlaySaveResult{}, fmt.Errorf("parse: %w", err)
	}
	abs := filepath.Join(s.Workspace.OverlayDir, clean)
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return OverlaySaveResult{}, err
	}
	if err := os.WriteFile(abs, []byte(p.Source), 0o600); err != nil {
		return OverlaySaveResult{}, err
	}
	// Re-import overlay so subsequent RPCs see the fresh state.
	o := semantic_overlay.NewOverlay()
	if _, err := o.Load(s.Workspace.OverlayDir); err != nil {
		return OverlaySaveResult{}, err
	}
	s.ReplaceOverlay(o)
	sum := sha256.Sum256([]byte(p.Source))
	return OverlaySaveResult{
		Path:         abs,
		BytesWritten: len(p.Source),
		SHA256:       hex.EncodeToString(sum[:]),
	}, nil
}

// EntityProvenanceParams identifies the entity to drill into. At least one
// of EntityID or QualifiedName must be set; EntityID wins when both are.
type EntityProvenanceParams struct {
	EntityID      string `json:"entity_id,omitempty"`
	QualifiedName string `json:"qualified_name,omitempty"`
}

// EntityProvenanceResult is the per-entity drill-down envelope. The
// embedded EntityView is the canonical render template defined by
// code_core.EntityView (per-entity row + folded ProvenanceSummary +
// canonical-ordered SourceEntry list); Resolved is the kernel sequence
// the lookup was taken at so the surface can show drift age.
type EntityProvenanceResult struct {
	View     code_core.EntityView `json:"view"`
	Resolved uint64               `json:"resolved_at_kernel_seq"`
}

// EntityProvenance handles entity.provenance — surfaces the merged
// provenance record (live LSP / SCIP / tree-sitter claims) for one
// code.core entity. Used by Studio's entity drill-down and the MCP
// gh://entity/code.core/<kind>/<id> resource (P1.I/T35, P1.T38).
//
// EntityID takes precedence; if missing, QualifiedName is resolved into
// an EntityID via the existing code.core lookup, then routed through
// the canonical LookupEntity API. Returns ErrEntityNotFound (wrapped)
// when the entity does not exist so callers can map it to a clear
// 404-shaped error.
func (s *Service) EntityProvenance(ctx context.Context, p EntityProvenanceParams) (EntityProvenanceResult, error) {
	if p.EntityID == "" && p.QualifiedName == "" {
		return EntityProvenanceResult{Resolved: s.Log.LastSeq()}, errors.New("entity_id or qualified_name required")
	}
	args, err := json.Marshal(codeLookupEntityArgs{ID: p.EntityID, QualifiedName: p.QualifiedName})
	if err != nil {
		return EntityProvenanceResult{}, err
	}
	env, err := s.Router().Route(ctx, kernel.RouteRequest{
		Layer:      "code.core",
		Capability: CapCodeLookupEntity,
		Args:       args,
	})
	if err != nil {
		if errors.Is(err, code_core.ErrEntityNotFound) {
			return EntityProvenanceResult{Resolved: s.Log.LastSeq()}, fmt.Errorf("entity not found: %w", code_core.ErrEntityNotFound)
		}
		return EntityProvenanceResult{Resolved: s.Log.LastSeq()}, err
	}
	var view code_core.EntityView
	if err := json.Unmarshal(env.Data, &view); err != nil {
		return EntityProvenanceResult{}, err
	}
	return EntityProvenanceResult{View: view, Resolved: env.ResolvedAtSeq}, nil
}

// MCPBeforeEditParams targets a selector for pre-edit snapshot capture.
type MCPBeforeEditParams struct {
	Selector string `json:"selector"`
}

// MCPBeforeEditResult is the snapshot returned to the agent.
type MCPBeforeEditResult struct {
	Selector            string                             `json:"selector"`
	ResolvedAtKernelSeq uint64                             `json:"resolved_at_kernel_seq"`
	Outcome             string                             `json:"outcome"`
	Matches             []semantic_overlay.ResolutionMatch `json:"matches"`
	Snapshot            map[string]string                  `json:"snapshot"` // entity_id → body_hash at this seq
}

// MCPBeforeEdit handles mcp.before_edit (P1.T38).
func (s *Service) MCPBeforeEdit(ctx context.Context, p MCPBeforeEditParams) (MCPBeforeEditResult, error) {
	if p.Selector == "" {
		return MCPBeforeEditResult{}, errors.New("selector required")
	}
	env, err := s.Overlay().Resolve(ctx, p.Selector, s.Code, s.Log.LastSeq())
	if err != nil {
		return MCPBeforeEditResult{}, err
	}
	snap := map[string]string{}
	for _, m := range env.Matches {
		ent, err := s.Code.LookupByQualifiedName(ctx, m.QualifiedName)
		if err != nil || ent == nil {
			continue
		}
		snap[m.EntityID] = ent.BodyHash
	}
	return MCPBeforeEditResult{
		Selector:            p.Selector,
		ResolvedAtKernelSeq: env.ResolvedAt,
		Outcome:             string(env.Outcome),
		Matches:             env.Matches,
		Snapshot:            snap,
	}, nil
}

// MCPAfterEditParams hands a unified diff back to the validator.
type MCPAfterEditParams struct {
	Diff string `json:"diff"`
}

// MCPAfterEdit handles mcp.after_edit (P1.T38). It forwards into validate.diff
// and tags the result so the agent can correlate it with the prior before_edit.
func (s *Service) MCPAfterEdit(ctx context.Context, p MCPAfterEditParams) (*change_process.ValidateDiffResult, error) {
	return s.ValidateDiff(ctx, ValidateDiffParams(p))
}

// ImpactedFlowsParams is the mcp.impacted_flows / impacted_flows tool input.
// Exactly one of Diff or Selector must be non-empty. Diff is a unified-diff
// blob (same shape as validate.diff); Selector is a named selector from the
// workspace overlay. (P2.T41)
type ImpactedFlowsParams struct {
	Diff     string `json:"diff,omitempty"`
	Selector string `json:"selector,omitempty"`
}

// FlowImpactRef is one flow surfaced by impacted_flows. TouchedEntityCount
// counts entities in the flow's resolved scope that overlap with the
// impacted set (touched + framework dependents).
type FlowImpactRef struct {
	Name                string `json:"name"`
	ScopeSelector       string `json:"scope_selector"`
	TouchedEntityCount  int    `json:"touched_entity_count"`
	Description         string `json:"description,omitempty"`
}

// ImpactedFlowsResult is the mcp.impacted_flows reply. Flows is the
// filtered, deterministically-ordered list of flows whose scope
// resolution overlaps the impacted set; Evidence carries the full
// framework-context records (producer + dependents) for every reachable
// producer in BFS order. (P2.T41)
type ImpactedFlowsResult struct {
	ResolvedAtKernelSeq uint64                            `json:"resolved_at_kernel_seq"`
	Flows               []FlowImpactRef                   `json:"flows"`
	Evidence            []change_process.FrameworkContext `json:"evidence"`
}

// ImpactedFlows handles mcp.impacted_flows (P2.T41). For diff input, runs
// the Stage 1-5 pipeline path to produce touched + impacted sets; for
// selector input, projects the selector to entity IDs and walks the same
// BFS. Then enumerates overlay flows and reports those whose declared
// scope contains any impacted entity.
//
// Reuse: the BFS lives entirely in change_process.Pipeline.ImpactedFromDiff
// / ImpactedFromTouched. This handler is a pure orchestrator.
func (s *Service) ImpactedFlows(ctx context.Context, p ImpactedFlowsParams) (ImpactedFlowsResult, error) {
	diffSet := strings.TrimSpace(p.Diff) != ""
	selSet := strings.TrimSpace(p.Selector) != ""
	if diffSet == selSet {
		return ImpactedFlowsResult{}, errors.New("exactly one of diff or selector required")
	}
	atSeq := s.Log.LastSeq()
	overlay := s.Overlay()
	pipe := &change_process.Pipeline{Overlay: overlay, Code: s.Code}

	var (
		touchedIDs map[string]struct{}
		producers  []change_process.FrameworkContext
		err        error
	)
	if diffSet {
		_, touchedIDs, producers, err = pipe.ImpactedFromDiff(ctx, []byte(p.Diff))
		if err != nil {
			return ImpactedFlowsResult{ResolvedAtKernelSeq: atSeq}, err
		}
	} else {
		env, rerr := overlay.Resolve(ctx, p.Selector, s.Code, atSeq)
		if rerr != nil {
			return ImpactedFlowsResult{ResolvedAtKernelSeq: atSeq}, rerr
		}
		seeds := make([]string, 0, len(env.Matches))
		for _, m := range env.Matches {
			if m.EntityID != "" {
				seeds = append(seeds, m.EntityID)
			}
		}
		touchedIDs, producers, err = pipe.ImpactedFromTouched(ctx, seeds)
		if err != nil {
			return ImpactedFlowsResult{ResolvedAtKernelSeq: atSeq}, err
		}
	}

	// impactedIDs = touched ∪ every dependent producer + dep id reached.
	impactedIDs := map[string]struct{}{}
	for id := range touchedIDs {
		impactedIDs[id] = struct{}{}
	}
	for _, fc := range producers {
		if fc.TouchedSubject.ID != "" {
			impactedIDs[fc.TouchedSubject.ID] = struct{}{}
		}
		for _, d := range fc.Dependents {
			if d.ID != "" {
				impactedIDs[d.ID] = struct{}{}
			}
		}
	}

	// Enumerate flows whose scope resolves to any entity in impactedIDs.
	flows := make([]FlowImpactRef, 0, len(overlay.Flows))
	for name, fl := range overlay.Flows {
		if fl == nil || fl.Scope == "" {
			continue
		}
		env, rerr := overlay.Resolve(ctx, fl.Scope, s.Code, atSeq)
		if rerr != nil || env == nil {
			continue
		}
		hits := 0
		for _, m := range env.Matches {
			if _, ok := impactedIDs[m.EntityID]; ok {
				hits++
			}
		}
		if hits == 0 {
			continue
		}
		flows = append(flows, FlowImpactRef{
			Name:               name,
			ScopeSelector:      fl.Scope,
			TouchedEntityCount: hits,
			Description:        fl.Description,
		})
	}
	sort.Slice(flows, func(i, j int) bool { return flows[i].Name < flows[j].Name })

	return ImpactedFlowsResult{
		ResolvedAtKernelSeq: atSeq,
		Flows:               flows,
		Evidence:            producers,
	}, nil
}

// ConflictsListResult is the conflicts.list reply (P1.I — TUI Conflicts panel).
type ConflictsListResult struct {
	Conflicts []ConflictRecord `json:"conflicts"`
}

// ConflictsList handles conflicts.list.
func (s *Service) ConflictsList(_ context.Context) (ConflictsListResult, error) {
	return ConflictsListResult{Conflicts: s.Conflicts()}, nil
}

// DaemonShutdown handles daemon.shutdown.
func (s *Service) DaemonShutdown(_ context.Context) (ReviewAck, error) {
	s.Shutdown()
	return ReviewAck{OK: true}, nil
}

// SnapshotCreateParams is the input shape for kernel.snapshot
// (F12 / P0.T11). Layer scopes the captured event set; pass "" for
// every layer. Seq=0 means "current head".
type SnapshotCreateParams struct {
	Layer string `json:"layer,omitempty"`
	Seq   uint64 `json:"seq,omitempty"`
}

// SnapshotCreate implements kernel.snapshot — captures a snapshot
// of the event log at the requested seq (or head when seq=0). The
// returned SnapshotHandle is opaque; clients pass it back to
// kernel.restoreSnapshot or use it to drive Compact-style
// recoverability proofs.
func (s *Service) SnapshotCreate(ctx context.Context, p SnapshotCreateParams) (kernel.SnapshotHandle, error) {
	if s.Log == nil {
		return kernel.SnapshotHandle{}, errors.New("snapshot: event log not available")
	}
	return s.Log.CreateSnapshot(ctx, p.Layer, p.Seq)
}

// SnapshotListParams allows filtering by layer; empty layer lists
// every snapshot in the workspace.
type SnapshotListParams struct {
	Layer string `json:"layer,omitempty"`
}

// SnapshotInfo is the per-row wire shape returned by kernel.listSnapshots.
type SnapshotInfo struct {
	ID        string `json:"id"`
	Seq       uint64 `json:"seq"`
	Layer     string `json:"layer"`
	CreatedAt string `json:"created_at"`
}

// SnapshotListResult wraps the list of snapshots.
type SnapshotListResult struct {
	Snapshots []SnapshotInfo `json:"snapshots"`
}

// SnapshotList implements kernel.listSnapshots — reports every
// captured snapshot for the workspace, optionally filtered to a
// single layer. F12.
func (s *Service) SnapshotList(ctx context.Context, p SnapshotListParams) (SnapshotListResult, error) {
	if s.Log == nil {
		return SnapshotListResult{Snapshots: []SnapshotInfo{}}, nil
	}
	rows, err := s.Log.ListSnapshots(ctx, p.Layer)
	if err != nil {
		return SnapshotListResult{}, err
	}
	out := make([]SnapshotInfo, len(rows))
	for i, r := range rows {
		out[i] = SnapshotInfo{ID: r.ID, Seq: r.Seq, Layer: r.Layer, CreatedAt: r.CreatedAt}
	}
	return SnapshotListResult{Snapshots: out}, nil
}

// SnapshotRestoreParams names the snapshot to replay.
type SnapshotRestoreParams struct {
	ID string `json:"id"`
}

// SnapshotRestoreResult reports the count of events restored.
type SnapshotRestoreResult struct {
	EventsRestored int `json:"events_restored"`
}

// SnapshotRestore implements kernel.restoreSnapshot — replays the
// snapshot's events into the live log. **Heavy operation:** pauses
// the writer for the duration; should never be invoked while users
// are actively editing the workspace. Use with care.
func (s *Service) SnapshotRestore(ctx context.Context, p SnapshotRestoreParams) (SnapshotRestoreResult, error) {
	if s.Log == nil {
		return SnapshotRestoreResult{}, errors.New("snapshot: event log not available")
	}
	if p.ID == "" {
		return SnapshotRestoreResult{}, errors.New("snapshot id required")
	}
	events, err := s.Log.LoadSnapshot(ctx, kernel.SnapshotHandle{ID: p.ID})
	if err != nil {
		return SnapshotRestoreResult{}, err
	}
	for i := range events {
		events[i].Seq = 0
	}
	if _, err := s.Log.Append(ctx, events); err != nil {
		return SnapshotRestoreResult{}, err
	}
	return SnapshotRestoreResult{EventsRestored: len(events)}, nil
}

// DoctorReportResult is the wire shape returned by health.extractors
// (and by the MCP gh://doctor resource). It carries the raw
// []detect.Report slice — the same data that backs `graph-harness
// doctor --json --verbose`, but in the nested per-language form rather
// than the CLI's flattened tools[] envelope.
type DoctorReportResult struct {
	WorkspaceRoot string          `json:"workspace_root"`
	GeneratedAt   time.Time       `json:"generated_at"`
	Languages     []detect.Report `json:"languages"`
}

// AuditUntaggedResult is the wire shape returned by daemon.auditUntagged.
// Each field is a list of identifiers for rows whose kernel_seq_tag is
// zero — the canonical writer-monopoly violation signal. Empty lists
// mean strict mode is holding (F16 / SPEC §9.1).
type AuditUntaggedResult struct {
	Entities    []string `json:"entities"`
	Provenance  []string `json:"provenance"`
	Relations   []string `json:"relations"`
	TotalCount  int      `json:"total_count"`
	WorkspaceID string   `json:"workspace_id,omitempty"`
}

// AuditUntagged aggregates the three Audit* helpers on the code.core
// store and surfaces them as a single RPC. F16 / P1.5.T07 — gives
// operators a CLI surface to verify the strict-mode writer-monopoly
// invariant after a polyglot fixture index.
func (s *Service) AuditUntagged(ctx context.Context) (AuditUntaggedResult, error) {
	out := AuditUntaggedResult{
		Entities:   []string{},
		Provenance: []string{},
		Relations:  []string{},
	}
	if s.Workspace != nil {
		out.WorkspaceID = s.Workspace.ID
	}
	if s.Code == nil {
		return out, nil
	}
	ents, err := s.Code.AuditUntaggedRows(ctx, 0)
	if err != nil {
		return out, err
	}
	prov, err := s.Code.AuditUntaggedProvenance(ctx, 0)
	if err != nil {
		return out, err
	}
	rels, err := s.Code.AuditUntaggedRelations(ctx)
	if err != nil {
		return out, err
	}
	if ents != nil {
		out.Entities = ents
	}
	if prov != nil {
		out.Provenance = prov
	}
	if rels != nil {
		out.Relations = rels
	}
	out.TotalCount = len(out.Entities) + len(out.Provenance) + len(out.Relations)
	return out, nil
}

// DoctorReport runs detection over the workspace's registered language
// detectors and returns the per-language reports. Used by the daemon
// /health/extractors endpoint and the MCP gh://doctor resource. P1.L
// (T52/T53). The detector probe runs each call — detection is cheap
// (sub-second on a warm cache) and the daemon does not yet hold a
// cached Orchestrator handle to memoize against; we re-probe each time
// so consumers always see the current environment.
func (s *Service) DoctorReport(ctx context.Context) (DoctorReportResult, error) {
	root := ""
	if s.Workspace != nil {
		root = s.Workspace.Root
	}
	registry := detect.NewRegistry()
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	reports, err := registry.ProbeAll(probeCtx, root)
	if err != nil {
		return DoctorReportResult{}, err
	}
	return DoctorReportResult{
		WorkspaceRoot: root,
		GeneratedAt:   time.Now().UTC(),
		Languages:     reports,
	}, nil
}
