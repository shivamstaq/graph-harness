package extract

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/source_live"
	"github.com/shivamstaq/graph-harness/internal/source_live/detect"
	"github.com/shivamstaq/graph-harness/internal/source_live/lsp"
	"github.com/shivamstaq/graph-harness/internal/source_live/scip"
)

// LSP timeouts. Cold-start is the slow path (gopls indexes the
// workspace; pyright spins up its analyzer). Per-file DocumentSymbol
// is fast once the server is warm. We bound both so a misbehaving or
// missing server doesn't stall the CLI past per-test e2e budgets.
//
// The per-call budget is generous (5s) because gopls's first
// DocumentSymbol after Initialize can race with workspace load —
// returning an empty result or stalling briefly until imports are
// resolved. A short budget there used to fast-disable the language
// for the rest of the sweep on the very first file, which was the
// "LSP fast-fails in 1s without producing symbols" symptom T-core
// caught. With 5s we let gopls recover; the global cap stays bounded
// at lspMaxConsecutiveTimeouts before the language is suspended for
// the workspace sweep.
const (
	lspInitTimeout            = 8 * time.Second
	lspPerCallTimeout         = 5 * time.Second
	lspMaxConsecutiveTimeouts = 3
)

// Orchestrator is the source.live → code.core production wiring per
// SPEC §6.11. It walks the workspace, gathers Symbols from every
// available fact source, and routes them through code_core.Unifier
// so each entity carries provenance.sources[] reflecting which sources
// observed it.
//
// Lifecycle:
//
//   - NewOrchestrator builds the host + refresher + scip-index cache
//     once per workspace open.
//   - IndexAll runs the full sweep (used by `selectors test`,
//     `validate-diff`, the daemon's initial workspace warm-up).
//   - IndexFile runs the per-file path (used by source_live.Watcher
//     on fsnotify FileChanged events).
//   - Close shuts down the LSP host + SCIP refresher cleanly.
//
// Concurrency: IndexAll/IndexFile may be called from multiple
// goroutines; per-file SCIP cache lookup is read-locked.
type Orchestrator struct {
	root             string
	store            *code_core.Store
	unifier          *code_core.Unifier
	host             *lsp.Host       // nil-safe
	refresh          *scip.Refresher // nil-safe
	enableTreesitter bool            // false = skip tree-sitter parsing entirely

	// refreshSeq returns the kernel head seq the SCIP refresh
	// consumer goroutine should stamp on freshly-imported symbols.
	// nil → use 0 (legacy non-watcher path).
	refreshSeq func() uint64
	refreshCtx context.Context //nolint:containedctx // F9: refresh consumer goroutine lifetime is tied to Close, not per-call
	refreshDone chan struct{}

	scipMu  sync.RWMutex
	scipMap map[string][]source_live.Symbol // workspace-relative path → SCIP-derived symbols

	lspMu                sync.Mutex
	lspDisabled          map[string]bool // languageID → "skip; we already tried and failed"
	lspConsecutiveCallTO map[string]int  // languageID → consecutive per-call timeouts since last success

	// Detection state (P1.L). Lazily populated on the first IndexAll /
	// IndexFile call so cheap CLI paths that never need detection
	// (e.g. health checks) don't pay the probe cost. Only exercised
	// when enableDetection is true (Options.EnableDetection); off by
	// default so one-shot CLI commands aren't subject to subprocess
	// probe cost. Long-running surfaces (daemon, doctor) opt in.
	detectRegistry  *detect.Registry
	detectEvents    *code_core.ExtractorUnavailableEmitter
	detectOnce      sync.Once
	detectMu        sync.RWMutex
	detectReports   []detect.Report
	enableDetection bool
}

// Options controls which sources the Orchestrator consults. Setters
// default to "enabled if discoverable"; pass any of the Disable*
// fields to skip a source unconditionally (useful in tests + the
// SPEC §6.11 build-order-tolerance specs that exercise single-source
// behaviour).
type Options struct {
	DisableLSP        bool
	DisableSCIP       bool
	DisableTreesitter bool

	// EnableDetection turns on the source.live detector probe inside
	// IndexAll so code.core.ExtractorUnavailable events are emitted
	// when primary extractors are missing. OFF by default because the
	// detector spawns multiple subprocess probes (bun/pnpm/yarn/pipx/
	// uv/npm) and each one-shot CLI command paying that cost would
	// noticeably slow down `selectors test` / `validate-diff` /
	// `code provenance`. Long-running surfaces (daemon, doctor command,
	// MCP gh://doctor handler) opt in. The doctor command runs its own
	// detector independently, so it doesn't need this either.
	EnableDetection bool
}

// NewOrchestrator wires the sources up. It does not spawn any LSP
// servers or read any .scip files yet — both are lazy. The returned
// Orchestrator must be Close()d when done so any spawned LSP
// subprocesses are reaped.
func NewOrchestrator(root string, store *code_core.Store, unifier *code_core.Unifier, opts Options) (*Orchestrator, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("orchestrator root: %w", err)
	}
	if store == nil {
		return nil, fmt.Errorf("orchestrator: store is nil")
	}
	if unifier == nil {
		return nil, fmt.Errorf("orchestrator: unifier is nil")
	}
	o := &Orchestrator{
		root:                 abs,
		store:                store,
		unifier:              unifier,
		enableTreesitter:     !opts.DisableTreesitter,
		scipMap:              make(map[string][]source_live.Symbol),
		lspDisabled:          make(map[string]bool),
		lspConsecutiveCallTO: make(map[string]int),
		detectRegistry:       detect.NewRegistry(),
		detectEvents:         code_core.NewExtractorUnavailableEmitter(unifier.Emitter),
		enableDetection:      opts.EnableDetection,
	}
	if !opts.DisableLSP {
		o.host = lsp.NewHost(lsp.NewRegistry(), abs)
	}
	if !opts.DisableSCIP {
		ref, err := scip.NewRefresher(abs, scip.DefaultImporters())
		if err != nil {
			return nil, fmt.Errorf("scip refresher: %w", err)
		}
		o.refresh = ref
	}
	return o, nil
}

// detectionTimeout caps the entire registry probe sweep so a stuck
// subprocess probe (bun pm bin, npm root -g, etc.) cannot freeze
// indexing. Per-probe timeout in detect.realExecer.Run is the
// fine-grained guard; this is the belt-and-braces overall cap.
const detectionTimeout = 4 * time.Second

// runDetection probes every registered detector exactly once per
// orchestrator instance. ExtractorUnavailable events are emitted
// idempotently for missing tools — repeated calls are no-ops. Errors
// from individual detectors are absorbed into the per-language Report
// so a single broken probe doesn't kill the indexing path.
//
// The full Reports slice is cached on the orchestrator so the daemon /
// MCP / doctor command can read it without re-probing.
//
// No-op when Options.EnableDetection is false (the default). One-shot
// CLI commands set EnableDetection=false so subprocess probe cost
// doesn't slow down `selectors test` / `validate-diff` /
// `code provenance`; long-running surfaces (daemon, MCP) opt in.
func (o *Orchestrator) runDetection(ctx context.Context) {
	if !o.enableDetection {
		return
	}
	o.detectOnce.Do(func() {
		if o.detectRegistry == nil {
			return
		}
		probeCtx, cancel := context.WithTimeout(ctx, detectionTimeout)
		defer cancel()
		reports, _ := o.detectRegistry.ProbeAll(probeCtx, o.root)
		o.detectMu.Lock()
		o.detectReports = reports
		o.detectMu.Unlock()
		// Emit ExtractorUnavailable events for every missing primary tool
		// (LSP / SCIP). Embedded parsers can never be missing, so they
		// are skipped by the emitter's Status filter.
		if o.detectEvents == nil {
			return
		}
		for _, r := range reports {
			for _, t := range r.Tools {
				_ = o.detectEvents.Emit(probeCtx, o.root, r.LanguageID, t)
			}
		}
	})
}

// DetectionReports returns the cached per-language detection reports.
// Returns nil before runDetection has been called. Used by daemon
// /health/extractors and MCP gh://doctor.
func (o *Orchestrator) DetectionReports() []detect.Report {
	o.detectMu.RLock()
	defer o.detectMu.RUnlock()
	return append([]detect.Report(nil), o.detectReports...)
}

// EnsureDetected runs detection if it hasn't been already. Surfaces
// (doctor command, daemon health endpoint) call this when they need
// the report without going through IndexAll.
func (o *Orchestrator) EnsureDetected(ctx context.Context) {
	o.runDetection(ctx)
}

// Close releases every spawned subprocess and watcher. Idempotent.
func (o *Orchestrator) Close(ctx context.Context) error {
	var firstErr error
	if o.host != nil {
		if err := o.host.Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if o.refresh != nil {
		o.refresh.Stop()
	}
	// F9: drain the SCIP refresh consumer goroutine. Stop() closes
	// the events channel, which makes the goroutine's range loop
	// exit; the close(o.refreshDone) inside the goroutine signals
	// us. Bounded wait to keep Close non-blocking under broken
	// refresher state.
	if o.refreshDone != nil {
		select {
		case <-o.refreshDone:
		case <-time.After(2 * time.Second):
		}
	}
	return firstErr
}

// PreloadSCIP scans the .scip-index/ directory once and caches each
// document's translated Symbols by relative path. Subsequent IndexFile
// calls look up the per-file slice directly without re-decoding the
// blob. Returns nil (no error, no warnings) when the workspace has no
// SCIP indexes — that's the build-order tolerance case.
func (o *Orchestrator) PreloadSCIP() error {
	if o.refresh == nil {
		return nil
	}
	paths, err := scip.FindIndexes(o.root)
	if err != nil {
		return fmt.Errorf("scip discover: %w", err)
	}
	if len(paths) == 0 {
		return nil
	}
	importers := scip.DefaultImporters()
	out := make(map[string][]source_live.Symbol)
	for _, p := range paths {
		idx, err := scip.Read(p)
		if err != nil {
			// Single bad index file shouldn't kill the workspace
			// indexer; log via the daemon eventually.
			continue
		}
		for _, imp := range importers {
			syms := imp.Import(idx, "")
			for _, s := range syms {
				rel := normalizeRel(s.Path)
				out[rel] = append(out[rel], s)
			}
		}
	}
	o.scipMu.Lock()
	o.scipMap = out
	o.scipMu.Unlock()
	return nil
}

// IndexAll walks the workspace and routes every supported source file
// through IndexFile. SCIP indexes are loaded once before the walk so
// per-file lookups are O(1). Files referenced by SCIP indexes but not
// present on disk (e.g. cross-repo references, vendored sources) are
// also routed through IndexFile so their SCIP-derived symbols still
// land in code.core — IndexFile handles the missing-source-file case
// gracefully.
//
// Detection (SPEC §6.18) runs once before the sweep so missing-tool
// signals reach the event log before any symbols are unified, letting
// downstream consumers attach low_confidence flags consistently.
func (o *Orchestrator) IndexAll(ctx context.Context, seq uint64) error {
	o.runDetection(ctx)
	if err := o.PreloadSCIP(); err != nil {
		return err
	}
	seen := make(map[string]struct{})
	files, err := SourceFilesUnder(o.root)
	if err != nil {
		return err
	}
	for _, abs := range files {
		rel, _ := filepath.Rel(o.root, abs)
		seen[rel] = struct{}{}
		if err := o.IndexFile(ctx, rel, seq); err != nil {
			return err
		}
	}
	// Union with SCIP-only paths so indexes describing files not on the
	// local disk still produce entities.
	o.scipMu.RLock()
	scipPaths := make([]string, 0, len(o.scipMap))
	for rel := range o.scipMap {
		scipPaths = append(scipPaths, rel)
	}
	o.scipMu.RUnlock()
	for _, rel := range scipPaths {
		if _, ok := seen[rel]; ok {
			continue
		}
		if err := o.IndexFile(ctx, rel, seq); err != nil {
			return err
		}
	}
	return nil
}

// IndexFile gathers Symbols for a single workspace-relative path from
// every enabled source and runs them through Unifier.Unify. Files
// that fail to parse are skipped silently — fsnotify storms during
// editor saves shouldn't surface as errors.
//
// When all three sources are disabled the call is a no-op (no file
// entity, no symbol unification). Tests use that mode to validate
// build-order tolerance from the inverse direction; production
// callers always have at least one source enabled.
//
// The File entity is written when tree-sitter is enabled (its source
// of truth for Files); when tree-sitter is disabled but LSP / SCIP
// describe the file, the LSP / SCIP language is used. The Unifier
// intentionally doesn't materialize Files itself — this is the only
// place File entities enter code.core.
func (o *Orchestrator) IndexFile(ctx context.Context, rel string, seq uint64) error {
	_, err := o.IndexFileChanged(ctx, rel, seq)
	return err
}

// IndexFileChanged is IndexFile with an explicit state-transition
// signal. changed=true iff materializing the symbols for rel caused
// at least one entity or provenance row to actually change. The
// daemon's watch loop drives drift-event emission off this signal:
// re-extracting an unchanged file produces zero kernel events, per
// SPEC §6.21. Callers that only need the side effect can call
// IndexFile.
//
// SPEC §6.20 cold-start drift scan: when the on-disk content hash
// matches the stored File entity's content_hash, IndexFileChanged
// skips the full parse + unify path and returns (false, nil). This
// is the cheap path that makes cold-start hydration O(stat) rather
// than O(re-extract everything). The fast-path only applies when
// SCIP is disabled — SCIP-only ingestion is content-independent of
// the local file (cross-repo references, vendored sources).
func (o *Orchestrator) IndexFileChanged(ctx context.Context, rel string, seq uint64) (bool, error) {
	abs := filepath.Join(o.root, rel)
	data, _ := os.ReadFile(abs) //nolint:gosec // rel is workspace-relative under controlled root; missing file is OK for SCIP-only ingestion

	// Cold-start drift-scan fast-path: when SCIP isn't contributing
	// to this file (no scipSyms registered for rel) AND we have a
	// stored content hash that equals the current disk content, the
	// re-extract is provably a no-op. Skip the parse + unify cost.
	if len(data) > 0 {
		o.scipMu.RLock()
		hasSCIP := len(o.scipMap[rel]) > 0
		o.scipMu.RUnlock()
		if !hasSCIP {
			currentHash := code_core.FileContentHash(data)
			stored, present, err := o.store.GetFileContentHash(ctx, rel)
			if err == nil && present && stored == currentHash {
				return false, nil
			}
		}
	}

	var (
		syms     []source_live.Symbol
		language string
	)

	if o.enableTreesitter && len(data) > 0 {
		pf, err := source_live.ParseFile(rel, data)
		if err == nil && pf != nil {
			language = pf.Language
			syms = append(syms, ParsedFileToSymbols(pf)...)
		}
	}

	if o.host != nil {
		hostLang := language
		if hostLang == "" {
			hostLang = source_live.LanguageOf(rel)
		}
		if hostLang != "" {
			if lspSyms, ok := o.gatherLSP(ctx, hostLang, rel); ok {
				// When tree-sitter is also active, drop LSP-emitted
				// Function / Method symbols. Per-language signature
				// rendering differs between gopls / tsserver / pyright
				// and our tree-sitter parsers (gopls puts `func(…)…`
				// in Detail; tree-sitter emits `(…)…`), which makes
				// the §6.12 normalized-signature hash diverge in a
				// non-trivial subset of cases — duplicate entity rows
				// would break unique-cardinality selectors. Tree-sitter
				// is source-of-truth for function-level extraction;
				// LSP retains live_lsp provenance on Class / Interface
				// / File entities until per-language signature
				// post-processing is fully aligned (P2 polish).
				if o.enableTreesitter {
					lspSyms = filterOutFunctions(lspSyms)
				}
				syms = append(syms, lspSyms...)
				if language == "" {
					language = hostLang
				}
			}
		}
	}

	o.scipMu.RLock()
	scipSyms := append([]source_live.Symbol(nil), o.scipMap[rel]...)
	o.scipMu.RUnlock()
	if len(scipSyms) > 0 {
		syms = append(syms, scipSyms...)
		if language == "" {
			language = scipSyms[0].LanguageID
		}
	}

	anyChanged := false

	// File entity (only when at least one source observed the file).
	if language != "" {
		fileEnt := code_core.Entity{
			ID:            code_core.FileID(rel),
			Kind:          code_core.KindFile,
			LanguageID:    language,
			QualifiedName: rel,
			Path:          rel,
		}
		entChanged, err := o.store.PutEntityIfChanged(ctx, fileEnt, seq)
		if err != nil {
			return false, fmt.Errorf("put file entity: %w", err)
		}
		provChanged, err := o.store.UpsertProvenanceIfChanged(ctx, fileEnt.ID, fileEntryFor(o, seq))
		if err != nil {
			return false, fmt.Errorf("provenance file entity: %w", err)
		}
		anyChanged = anyChanged || entChanged || provChanged
	}

	if len(syms) > 0 {
		_, symsChanged, err := o.unifier.UnifyChanged(ctx, syms, seq)
		if err != nil {
			return false, fmt.Errorf("unify %s: %w", rel, err)
		}
		anyChanged = anyChanged || symsChanged
	}

	// Record the new content hash on the File entity so the next
	// cold-start drift scan can skip this path when its disk content
	// matches. Only relevant when we actually have file content;
	// SCIP-only paths (no on-disk file) keep their stored hash as-is.
	if len(data) > 0 && language != "" {
		if err := o.store.SetFileContentHash(ctx, rel, code_core.FileContentHash(data)); err != nil {
			// Non-fatal: the drift scan degrades to "always re-extract"
			// when SetFileContentHash fails, which is annoying but
			// correct.
			return anyChanged, fmt.Errorf("set content hash %s: %w", rel, err)
		}
	}

	return anyChanged, nil
}

// filterOutFunctions returns syms with all Function and Method kinds
// removed. See the call site in IndexFile for the rationale: tree-sitter
// owns function-level extraction in v1; LSP keeps File / Class /
// Interface symbols.
func filterOutFunctions(syms []source_live.Symbol) []source_live.Symbol {
	out := syms[:0]
	for _, s := range syms {
		if s.Kind == source_live.SymbolKindFunction || s.Kind == source_live.SymbolKindMethod {
			continue
		}
		out = append(out, s)
	}
	return out
}

// fileEntryFor picks the SourceEntry attribution for the synthetic
// File entity based on which sources are enabled. Tree-sitter wins
// when enabled (it's the source-of-truth for File-level facts);
// otherwise the first enabled source claims attribution so the
// build-order-tolerance specs that disable tree-sitter still see
// File-level provenance reflecting the active source.
func fileEntryFor(o *Orchestrator, seq uint64) code_core.SourceEntry {
	switch {
	case o.enableTreesitter:
		return treesitterFileEntry(seq)
	case o.host != nil:
		return code_core.SourceEntry{
			SourceClass: code_core.SourceClassLSP,
			Confidence:  1.0,
			LastSeenSeq: seq,
			Freshness:   code_core.FreshnessLive,
			ProducedBy:  "extractor:lsp",
		}
	default:
		return code_core.SourceEntry{
			SourceClass: code_core.SourceClassSCIP,
			Confidence:  1.0,
			LastSeenSeq: seq,
			Freshness:   code_core.FreshnessCurrent,
			ProducedBy:  "extractor:scip",
		}
	}
}

// gatherLSP fetches DocumentSymbol facts for a single (language, file)
// with bounded timeouts so a slow/missing LSP server cannot stall the
// indexing path past per-test e2e budgets. After the first failure for
// a language the orchestrator stops asking that language altogether
// for the rest of the workspace sweep — gopls cold-start failures
// typically affect every subsequent file too, so retrying just wastes
// the budget.
func (o *Orchestrator) gatherLSP(ctx context.Context, languageID, rel string) ([]source_live.Symbol, bool) {
	o.lspMu.Lock()
	if o.lspDisabled[languageID] {
		o.lspMu.Unlock()
		return nil, false
	}
	o.lspMu.Unlock()

	initCtx, cancelInit := context.WithTimeout(ctx, lspInitTimeout)
	driver, err := o.host.DriverFor(initCtx, languageID)
	cancelInit()
	if err != nil {
		o.lspMu.Lock()
		o.lspDisabled[languageID] = true
		o.lspMu.Unlock()
		return nil, false
	}

	callCtx, cancelCall := context.WithTimeout(ctx, lspPerCallTimeout)
	defer cancelCall()
	syms, err := driver.DocumentSymbol(callCtx, rel)
	if err != nil {
		// Single-file failures don't disable the language — the next
		// file might be fine. Repeated consecutive timeouts suggest
		// the server is genuinely stuck; suspend after
		// lspMaxConsecutiveTimeouts so we don't burn the rest of the
		// workspace budget on a hung server. Any non-timeout error
		// resets the counter (it's a real refusal, not slowness).
		if errors.Is(err, context.DeadlineExceeded) {
			o.lspMu.Lock()
			o.lspConsecutiveCallTO[languageID]++
			if o.lspConsecutiveCallTO[languageID] >= lspMaxConsecutiveTimeouts {
				o.lspDisabled[languageID] = true
			}
			o.lspMu.Unlock()
		}
		return nil, false
	}
	// Successful call resets the consecutive-timeout counter.
	o.lspMu.Lock()
	o.lspConsecutiveCallTO[languageID] = 0
	o.lspMu.Unlock()
	return syms, true
}

// AvailableLSPLanguages returns the LSP-served languages discoverable
// in the current environment. Empty when LSP is disabled or no servers
// are on $PATH. Useful for status / doctor reporting.
func (o *Orchestrator) AvailableLSPLanguages() []string {
	if o.host == nil {
		return nil
	}
	return o.host.AvailableLanguages()
}

// SetLSPNotificationHandler installs an LSP push-back callback on the
// orchestrator's host. The daemon installs a handler that translates
// every server-initiated notification (publishDiagnostics, $/progress,
// documentSymbol push variants) into a code.core drift event on the
// kernel bus via the P0.5.T01 push channel. P1.5.T02.
//
// Safe no-op when LSP is disabled. Drivers spawned after this call
// inherit the handler; already-spawned drivers receive it immediately
// through the host.
func (o *Orchestrator) SetLSPNotificationHandler(h lsp.NotificationHandler) {
	if o.host == nil {
		return
	}
	o.host.SetNotificationHandler(h)
}

// SCIPRefreshHook fires once per consumed SCIP refresh Event. The
// daemon installs a hook that appends `code.core.SCIPRefreshed` on
// the kernel bus so subscribers see hot-reload activity end-to-end.
// Nil leaves the consumer goroutine silent.
type SCIPRefreshHook func(indexPath string, languageID string, symbolCount int)

// StartSCIPRefresh begins the F9 / P1.T11 SCIP hot-reload pipeline.
// The Refresher watches `.scip-index/` via fsnotify; this method
// kicks off Start + a consumer goroutine that drains Events() and
// routes the resulting Symbols through the unifier so a fresh
// .scip file landing in the workspace produces drift events without
// requiring an explicit re-index.
//
// seqFn returns the kernel head seq each Event should be stamped
// with (typically `log.LastSeq`). Pass nil to use a constant zero,
// which means "no event-log integration" — the symbols still flow
// through the unifier (so compare-before-emit suppresses no-ops),
// but the resulting drift events lose their kernel-seq attribution.
//
// hook fires once per consumed event (after unify) so the daemon
// can append a `code.core.SCIPRefreshed` event on the kernel bus
// for observability. Pass nil to keep the consumer silent.
//
// Idempotent: subsequent calls are no-ops once the consumer is
// running.
func (o *Orchestrator) StartSCIPRefresh(ctx context.Context, seqFn func() uint64, errLog func(format string, args ...any), hook SCIPRefreshHook) error {
	if o.refresh == nil {
		return nil
	}
	if err := o.refresh.Start(ctx); err != nil {
		return fmt.Errorf("scip refresher start: %w", err)
	}
	o.refreshCtx = ctx
	o.refreshSeq = seqFn
	o.refreshDone = make(chan struct{})
	go func() {
		defer close(o.refreshDone)
		for ev := range o.refresh.Events() {
			if ev.Err != nil {
				if errLog != nil {
					errLog("scip refresh: %v", ev.Err)
				}
				continue
			}
			seq := uint64(0)
			if seqFn != nil {
				seq = seqFn()
			}
			if _, _, err := o.unifier.UnifyChanged(ctx, ev.Symbols, seq); err != nil {
				if errLog != nil {
					errLog("scip refresh unify (%s): %v", ev.IndexPath, err)
				}
			}
			if hook != nil {
				hook(ev.IndexPath, ev.LanguageID, len(ev.Symbols))
			}
		}
	}()
	return nil
}

// HasSCIPIndexes reports whether the cached SCIP map covers any files.
// PreloadSCIP must have been called first.
func (o *Orchestrator) HasSCIPIndexes() bool {
	o.scipMu.RLock()
	defer o.scipMu.RUnlock()
	return len(o.scipMap) > 0
}

// SourceFilesUnder mirrors source_live.Watcher's file-walk pruning so
// initial-sweep and live-edit indexing see identical file sets. Public
// because the CLI also uses it for `selectors test --explain` and
// `validate-diff` paths that don't go through the orchestrator.
func SourceFilesUnder(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate transient errors
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") ||
				name == "vendor" || name == "node_modules" ||
				name == "bin" || name == "dist" || name == "build" ||
				name == "venv" || name == "__pycache__" ||
				name == "target" {
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if source_live.LanguageOf(path) != "" {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

// treesitterFileEntry returns the canonical SourceEntry for a
// tree-sitter-observed File at seq. Mirrors code_core's internal
// treesitterEntry helper.
func treesitterFileEntry(seq uint64) code_core.SourceEntry {
	return code_core.SourceEntry{
		SourceClass: code_core.SourceClassTreesitter,
		Confidence:  1.0,
		LastSeenSeq: seq,
		Freshness:   code_core.FreshnessLive,
		ProducedBy:  "extractor:treesitter",
	}
}

// normalizeRel ensures SCIP-supplied document paths use forward slashes
// (matching what filepath.Rel produces with ToSlash on the workspace
// walk side).
func normalizeRel(p string) string {
	return filepath.ToSlash(p)
}
