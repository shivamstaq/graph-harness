package jsonrpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/shivamstaq/graph-harness/internal/daemon"
	"github.com/shivamstaq/graph-harness/internal/dsl"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/review_queue"
	"github.com/shivamstaq/graph-harness/internal/semantic_overlay"
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

// SelectorsTest handles selectors.test.
func (s *Service) SelectorsTest(ctx context.Context, p SelectorsTestParams) (*semantic_overlay.ResolutionEnvelope, error) {
	if p.Name == "" {
		return nil, errors.New("selector name required")
	}
	o := s.Overlay()
	return o.Resolve(ctx, p.Name, s.Code, s.Log.LastSeq())
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

// SelectorsPreview handles selectors.preview.
func (s *Service) SelectorsPreview(ctx context.Context, p SelectorsPreviewParams) (SelectorsPreviewResult, error) {
	res := SelectorsPreviewResult{Outcome: "unresolved", Matches: []semantic_overlay.ResolutionMatch{}, Resolved: s.Log.LastSeq()}
	switch p.Kind {
	case "qualified_name":
		ent, err := s.Code.LookupByQualifiedName(ctx, p.Value)
		if err != nil {
			return res, err
		}
		if ent != nil {
			res.Outcome = "bound"
			res.Matches = append(res.Matches, semantic_overlay.ResolutionMatch{
				EntityID:      ent.ID,
				QualifiedName: ent.QualifiedName,
				Confidence:    1.0,
				ViaAnchor:     "qualified_name",
			})
		}
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

// FlowsList handles flows.list.
func (s *Service) FlowsList(_ context.Context) (FlowsListResult, error) {
	o := s.Overlay()
	names := make([]string, 0, len(o.Flows))
	for n := range o.Flows {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]FlowSummary, 0, len(names))
	for _, n := range names {
		f := o.Flows[n]
		out = append(out, FlowSummary{
			Name:        f.Name,
			Description: f.Description,
			Scope:       f.Scope,
			Steps:       len(f.Steps),
		})
	}
	return FlowsListResult{Flows: out}, nil
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

// EntityProvenanceParams identifies the entity to drill into.
type EntityProvenanceParams struct {
	EntityID      string `json:"entity_id,omitempty"`
	QualifiedName string `json:"qualified_name,omitempty"`
}

// EntityProvenanceResult is the per-entity drill-down envelope.
type EntityProvenanceResult struct {
	Entity   *code_core.Entity       `json:"entity"`
	Sources  []code_core.SourceEntry `json:"sources"`
	Resolved uint64                  `json:"resolved_at_kernel_seq"`
}

// EntityProvenance handles entity.provenance — surfaces the merged
// provenance record (live LSP / SCIP / tree-sitter claims) for one
// code.core entity. Used by Studio's entity drill-down (P1.I/T35).
func (s *Service) EntityProvenance(ctx context.Context, p EntityProvenanceParams) (EntityProvenanceResult, error) {
	res := EntityProvenanceResult{Resolved: s.Log.LastSeq(), Sources: []code_core.SourceEntry{}}
	var ent *code_core.Entity
	var err error
	switch {
	case p.EntityID != "":
		// Direct id lookup.
		ent, err = s.lookupByID(ctx, p.EntityID)
	case p.QualifiedName != "":
		ent, err = s.Code.LookupByQualifiedName(ctx, p.QualifiedName)
	default:
		return res, errors.New("entity_id or qualified_name required")
	}
	if err != nil {
		return res, err
	}
	if ent == nil {
		return res, fmt.Errorf("entity not found")
	}
	res.Entity = ent
	src, err := s.Code.GetProvenance(ctx, ent.ID)
	if err != nil {
		return res, err
	}
	res.Sources = src
	return res, nil
}

func (s *Service) lookupByID(ctx context.Context, id string) (*code_core.Entity, error) {
	// The store doesn't expose a direct LookupByID surface today; fall
	// through to qualified-name suffix when the id-shaped value happens
	// to look like a qualified name. Future work in code.core will add
	// a stable LookupByID. For now, return nil to surface a clear error.
	if strings.Contains(id, ".") {
		return s.Code.LookupByQualifiedNameSuffix(ctx, id)
	}
	return nil, fmt.Errorf("LookupByID not yet exposed by code.core; use qualified_name")
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
