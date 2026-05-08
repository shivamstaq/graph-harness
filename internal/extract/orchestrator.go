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
	"github.com/shivamstaq/graph-harness/internal/source_live/lsp"
	"github.com/shivamstaq/graph-harness/internal/source_live/scip"
)

// LSP timeouts. Cold-start is the slow path (gopls indexes the
// workspace; pyright spins up its analyzer). Per-file DocumentSymbol
// is fast once the server is warm. We bound both so a misbehaving or
// missing server doesn't stall the CLI past per-test e2e budgets.
const (
	lspInitTimeout    = 8 * time.Second
	lspPerCallTimeout = 2 * time.Second
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
	root    string
	store   *code_core.Store
	unifier *code_core.Unifier
	host    *lsp.Host       // nil-safe
	refresh *scip.Refresher // nil-safe

	scipMu  sync.RWMutex
	scipMap map[string][]source_live.Symbol // workspace-relative path → SCIP-derived symbols

	lspMu       sync.Mutex
	lspDisabled map[string]bool // languageID → "skip; we already tried and failed"
}

// Options controls which sources the Orchestrator consults. Setters
// default to "enabled if discoverable"; pass DisableLSP/DisableSCIP
// to skip a source unconditionally (useful in tests).
type Options struct {
	DisableLSP  bool
	DisableSCIP bool
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
		root:        abs,
		store:       store,
		unifier:     unifier,
		scipMap:     make(map[string][]source_live.Symbol),
		lspDisabled: make(map[string]bool),
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
// per-file lookups are O(1).
func (o *Orchestrator) IndexAll(ctx context.Context, seq uint64) error {
	if err := o.PreloadSCIP(); err != nil {
		return err
	}
	files, err := SourceFilesUnder(o.root)
	if err != nil {
		return err
	}
	for _, abs := range files {
		rel, _ := filepath.Rel(o.root, abs)
		if err := o.IndexFile(ctx, rel, seq); err != nil {
			return err
		}
	}
	return nil
}

// IndexFile gathers Symbols for a single workspace-relative path from
// every available source and runs them through Unifier.Unify. Files
// that fail to parse are skipped silently — fsnotify storms during
// editor saves shouldn't surface as errors.
//
// Always writes the File entity directly with tree-sitter provenance
// so file-level lookups don't depend on three-source agreement.
func (o *Orchestrator) IndexFile(ctx context.Context, rel string, seq uint64) error {
	abs := filepath.Join(o.root, rel)
	data, err := os.ReadFile(abs) //nolint:gosec // rel is workspace-relative under controlled root
	if err != nil {
		return nil //nolint:nilerr // unreadable file is a no-op for indexing
	}
	pf, err := source_live.ParseFile(rel, data)
	if err != nil || pf == nil {
		return nil
	}

	// File entity: written directly because the Unifier intentionally
	// does not materialize Files (only Functions / Methods / TypeDecls).
	fileEnt := code_core.Entity{
		ID:            code_core.FileID(rel),
		Kind:          code_core.KindFile,
		LanguageID:    pf.Language,
		QualifiedName: rel,
		Path:          rel,
	}
	if err := o.store.PutEntity(ctx, fileEnt, seq); err != nil {
		return fmt.Errorf("put file entity: %w", err)
	}
	if err := o.store.UpsertProvenance(ctx, fileEnt.ID, treesitterFileEntry(seq)); err != nil {
		return fmt.Errorf("provenance file entity: %w", err)
	}

	// Gather per-file Symbols from every source.
	syms := ParsedFileToSymbols(pf)

	if o.host != nil {
		if lspSyms, ok := o.gatherLSP(ctx, pf.Language, rel); ok {
			syms = append(syms, lspSyms...)
		}
	}

	o.scipMu.RLock()
	scipSyms := append([]source_live.Symbol(nil), o.scipMap[rel]...)
	o.scipMu.RUnlock()
	syms = append(syms, scipSyms...)

	if _, err := o.unifier.Unify(ctx, syms, seq); err != nil {
		return fmt.Errorf("unify %s: %w", rel, err)
	}
	return nil
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
		// file might be fine. But timeouts probably indicate broader
		// trouble; flag and skip the language to bound the budget.
		if errors.Is(err, context.DeadlineExceeded) {
			o.lspMu.Lock()
			o.lspDisabled[languageID] = true
			o.lspMu.Unlock()
		}
		return nil, false
	}
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
