package code_core

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/shivamstaq/graph-harness/internal/code_core/normalize"
	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// EventEmitter is the minimal sink the unifier needs to publish
// `code.core.SymbolDisambiguation` events. The shape is deliberately
// narrower than [facts.EventLog.Append] so this package does not pull
// the kernel/facts surface as a dependency — production wiring passes
// a closure that calls Append; tests pass a slice-collecting fake.
//
// The kind argument is the bare event kind ("SymbolDisambiguation"),
// without the layer prefix. Implementations are expected to namespace
// the event under "code.core" themselves.
type EventEmitter interface {
	EmitCodeCoreEvent(ctx context.Context, kind string, payload []byte) error
}

// EventEmitterFunc adapts a plain function to the [EventEmitter]
// interface. Convenience for production wiring where the call site
// already owns a closure.
type EventEmitterFunc func(ctx context.Context, kind string, payload []byte) error

// EmitCodeCoreEvent satisfies [EventEmitter].
func (f EventEmitterFunc) EmitCodeCoreEvent(ctx context.Context, kind string, payload []byte) error {
	return f(ctx, kind, payload)
}

// Unifier folds three-source [source_live.Symbol] streams into
// code.core entities + provenance per SPEC §6.11–§6.12.
//
// Behavior summary:
//
//   - Each Symbol maps to a canonical content-addressable ID via the
//     §6.12 formula for its kind. Symbols agreeing on (path, start_byte,
//     end_byte) are treated as describing the same logical entity.
//   - When all sources observing a logical entity produce the same
//     canonical ID, [Unify] writes a single row to `code_entities` and
//     one row per source to `code_entity_provenance`. The entity's
//     identity is the consensus ID.
//   - When sources disagree on the canonical ID for the same logical
//     entity (so the location range carries multiple distinct IDs), the
//     unifier writes one entity per distinct ID — each with the
//     provenance subset that produced it — and emits a
//     `code.core.SymbolDisambiguation` event listing every claim. The
//     emission is gated on [Unifier.DisagreementThreshold]: by default
//     ≥ 2 distinct IDs at one location triggers the event (configurable
//     in `manifests/code.core.yaml::unify.disagreement_threshold`).
//
// The unifier is stateless across [Unify] calls: it never reads the
// existing entities table. This keeps the algorithm a deterministic
// fold over the input batch and makes the property tests reproducible.
type Unifier struct {
	Store                 *Store
	Emitter               EventEmitter
	DisagreementThreshold int // 0 means "use default (2)"
}

// Default disagreement threshold. SPEC §5 risks: "Three-source
// disagreement noise on launch — gate auto-event-emission on ≥ 2
// sources disagreeing." 2 is the configured default in the manifest.
const defaultDisagreementThreshold = 2

// locKey identifies a logical source-text location across fact
// sources: every Symbol with the same (path, byte-range, kind) is
// considered to describe the same logical entity, even if its
// canonical ID differs. The kind discriminator keeps a Function and a
// Method that happen to share a byte range (e.g. a method declaration
// containing an inner closure) in distinct buckets.
type locKey struct {
	path      string
	startByte uint32
	endByte   uint32
	kind      source_live.SymbolKind
}

// idGroup accumulates every source's claim for a single canonical ID
// at a single location. canonSig caches the per-language normalized
// signature so the disambiguation payload can show what each source
// "saw" without recomputing.
type idGroup struct {
	entity   Entity
	sources  []SourceEntry
	canonSig string
}

// Unify ingests a batch of Symbols at the given event-log seq. The
// batch may contain Symbols from any subset of the three sources for
// any number of files. Returned entity IDs are deduplicated and
// returned in canonical (sorted) order so callers can compare across
// runs without an additional sort.
//
// For the change-aware variant the watcher → orchestrator path needs
// (SPEC §6.21 compare-before-emit), see UnifyChanged.
func (u *Unifier) Unify(ctx context.Context, syms []source_live.Symbol, seq uint64) ([]string, error) {
	ids, _, err := u.UnifyChanged(ctx, syms, seq)
	return ids, err
}

// UnifyChanged is Unify with an explicit state-transition signal.
// changed=true iff at least one entity or provenance row was either
// inserted or had its content fingerprint shift; refreshes of
// last_seen_seq alone do not count as transitions. The daemon's
// watch loop drives drift-event emission off this signal so re-
// extracting an unchanged file produces zero kernel events.
func (u *Unifier) UnifyChanged(ctx context.Context, syms []source_live.Symbol, seq uint64) ([]string, bool, error) {
	if u.Store == nil {
		return nil, false, fmt.Errorf("code_core.Unifier: Store is nil")
	}
	if len(syms) == 0 {
		return nil, false, nil
	}
	threshold := u.DisagreementThreshold
	if threshold <= 0 {
		threshold = defaultDisagreementThreshold
	}

	loc := make(map[locKey]map[string]*idGroup) // location → id → group

	for i := range syms {
		s := syms[i]
		ent, ok := entityFromSymbol(s)
		if !ok {
			// Files / unrecognized kinds: skip in v1; FileID rows are
			// produced from ParsedFile ingestion, not from the per-Symbol
			// path.
			continue
		}
		entry := SourceEntry{
			SourceClass: SourceClassFromSymbol(s.SourceClass),
			Confidence:  clampConfidence(s.Confidence),
			LastSeenSeq: seq,
			Freshness:   freshnessForSourceClass(s.SourceClass),
			ProducedBy:  s.ProducedBy,
		}
		if entry.SourceClass == "" {
			continue
		}
		k := locKey{path: s.Path, startByte: s.Range.StartByte, endByte: s.Range.EndByte, kind: s.Kind}
		if loc[k] == nil {
			loc[k] = make(map[string]*idGroup)
		}
		g, exists := loc[k][ent.ID]
		if !exists {
			g = &idGroup{entity: ent, canonSig: normalize.ForLanguage(s.LanguageID, s.Signature)}
			loc[k][ent.ID] = g
		}
		g.sources = append(g.sources, entry)
	}

	// Materialize. Sort location keys for deterministic write order so
	// tests asserting on emitted-event order are stable.
	locKeys := make([]locKey, 0, len(loc))
	for k := range loc {
		locKeys = append(locKeys, k)
	}
	sort.Slice(locKeys, func(i, j int) bool {
		if locKeys[i].path != locKeys[j].path {
			return locKeys[i].path < locKeys[j].path
		}
		if locKeys[i].startByte != locKeys[j].startByte {
			return locKeys[i].startByte < locKeys[j].startByte
		}
		return locKeys[i].endByte < locKeys[j].endByte
	})

	var written []string
	anyChanged := false
	for _, k := range locKeys {
		group := loc[k]
		distinctIDs := make([]string, 0, len(group))
		for id := range group {
			distinctIDs = append(distinctIDs, id)
		}
		sort.Strings(distinctIDs)

		for _, id := range distinctIDs {
			g := group[id]
			changed, err := u.writeEntity(ctx, g.entity, g.sources, seq)
			if err != nil {
				return nil, false, err
			}
			anyChanged = anyChanged || changed
			written = append(written, id)
		}

		if len(distinctIDs) >= threshold {
			if err := u.emitDisambiguation(ctx, k.path, k.startByte, k.endByte, group, seq); err != nil {
				return nil, false, err
			}
		}
	}
	return written, anyChanged, nil
}

// writeEntity persists e and its provenance, returning whether the
// stored state actually changed. changed=false signals a no-op
// re-observation (per SPEC §6.21 compare-before-emit) — callers may
// short-circuit downstream event emission accordingly. The
// transition reflects entity content only; per-source last_seen_seq
// refreshes are not classified as state transitions.
func (u *Unifier) writeEntity(ctx context.Context, e Entity, sources []SourceEntry, seq uint64) (bool, error) {
	entityChanged, err := u.Store.PutEntityIfChanged(ctx, e, seq)
	if err != nil {
		return false, fmt.Errorf("put entity %s: %w", e.ID, err)
	}
	provChanged := false
	for _, src := range sources {
		changed, err := u.Store.UpsertProvenanceIfChanged(ctx, e.ID, src)
		if err != nil {
			return false, fmt.Errorf("upsert provenance %s/%s: %w", e.ID, src.SourceClass, err)
		}
		if changed {
			provChanged = true
		}
	}
	return entityChanged || provChanged, nil
}

// SymbolDisambiguationPayload is the JSON shape emitted as the
// `code.core.SymbolDisambiguation` event payload. It records one
// claim per distinct canonical ID at a single source-text location, so
// downstream surfaces (TUI Conflicts panel, Studio per-source
// drill-down) can render every source's view side by side.
type SymbolDisambiguationPayload struct {
	Path      string                          `json:"path"`
	StartByte uint32                          `json:"start_byte"`
	EndByte   uint32                          `json:"end_byte"`
	Claims    []SymbolDisambiguationClaim     `json:"claims"`
	Threshold int                             `json:"threshold"`
	Resolved  *SymbolDisambiguationResolution `json:"resolved,omitempty"`
}

// SymbolDisambiguationClaim is one (canonical_id, source) pairing.
// MultiSources lists every source-class that produced this exact
// canonical_id; Signature is the per-language normalized signature
// observed by the first source in the list (useful for Studio diffs).
type SymbolDisambiguationClaim struct {
	CanonicalID  string        `json:"canonical_id"`
	Sources      []SourceEntry `json:"sources"`
	Signature    string        `json:"signature,omitempty"`
	QualifiedNm  string        `json:"qualified_name,omitempty"`
	LanguageID   string        `json:"language_id,omitempty"`
	BodyHash     string        `json:"body_hash,omitempty"`
	IsAnonymous  bool          `json:"is_anonymous,omitempty"`
	ParentID     string        `json:"parent_id,omitempty"`
	Ordinal      uint32        `json:"ordinal,omitempty"`
	OrdinalIsSet bool          `json:"ordinal_is_set,omitempty"`
}

// SymbolDisambiguationResolution is reserved for the change.process
// resolver to attach an outcome to a disambiguation event. The
// unifier itself never populates it — it is part of the event payload
// schema so subscribers can re-emit the resolved variant without a
// schema migration.
type SymbolDisambiguationResolution struct {
	WinnerID string `json:"winner_id"`
	Reason   string `json:"reason"`
}

func (u *Unifier) emitDisambiguation(
	ctx context.Context,
	path string, startByte, endByte uint32,
	group map[string]*idGroup,
	seq uint64,
) error {
	if u.Emitter == nil {
		return nil
	}
	threshold := u.DisagreementThreshold
	if threshold <= 0 {
		threshold = defaultDisagreementThreshold
	}
	claims := make([]SymbolDisambiguationClaim, 0, len(group))
	ids := make([]string, 0, len(group))
	for id := range group {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	// SPEC §6.21 suppress-at-source: skip re-emission when the same
	// set of canonical IDs has already claimed this (path, start, end)
	// location. The dedup index is keyed by the claims fingerprint so
	// a genuinely new disagreement (different IDs) still fires; only
	// idempotent re-observation of the same disagreement is suppressed.
	// Store.MarkDisambiguationEmitted returns false when a row already
	// exists for this fingerprint; we honor that as "do not emit."
	if u.Store != nil {
		hash := DisambiguationClaimsHash(ids)
		fresh, err := u.Store.MarkDisambiguationEmitted(ctx, path, startByte, endByte, hash, seq)
		if err != nil {
			return fmt.Errorf("disambiguation dedup: %w", err)
		}
		if !fresh {
			return nil
		}
	}
	for _, id := range ids {
		g := group[id]
		claims = append(claims, SymbolDisambiguationClaim{
			CanonicalID: id,
			Sources:     append([]SourceEntry(nil), g.sources...),
			Signature:   g.canonSig,
			QualifiedNm: g.entity.QualifiedName,
			LanguageID:  g.entity.LanguageID,
			BodyHash:    g.entity.BodyHash,
			IsAnonymous: g.entity.ParentID != "" && g.entity.QualifiedName == "",
			ParentID:    g.entity.ParentID,
			Ordinal:     g.entity.Ordinal,
		})
	}
	payload := SymbolDisambiguationPayload{
		Path:      path,
		StartByte: startByte,
		EndByte:   endByte,
		Claims:    claims,
		Threshold: threshold,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal SymbolDisambiguation payload: %w", err)
	}
	_ = seq // reserved for future "as_of_seq" stamping in the payload
	return u.Emitter.EmitCodeCoreEvent(ctx, "SymbolDisambiguation", buf)
}

// entityFromSymbol turns a source_live.Symbol into a code.core Entity
// with its canonical ID populated per SPEC §6.12. Returns ok=false for
// symbols that the v1 unifier does not materialize (Files — see
// IngestParsedFile — and unknown kinds).
func entityFromSymbol(s source_live.Symbol) (Entity, bool) {
	if s.IsAnonymous() {
		kindTag := s.KindTag()
		id := AnonymousID(s.ParentID, kindTag, s.Ordinal)
		return Entity{
			ID:         id,
			Kind:       entityKindFromSymbolKind(s.Kind),
			LanguageID: s.LanguageID,
			Path:       s.Path,
			BodyHash:   s.BodyHash,
			KindTag:    kindTag,
			ParentID:   s.ParentID,
			Ordinal:    s.Ordinal,
		}, true
	}
	switch s.Kind {
	case source_live.SymbolKindFunction:
		ns := normalize.ForLanguage(s.LanguageID, s.Signature)
		id := FunctionID(s.LanguageID, s.QualifiedName, ns)
		return Entity{
			ID:                  id,
			Kind:                KindFunction,
			LanguageID:          s.LanguageID,
			QualifiedName:       s.QualifiedName,
			Path:                s.Path,
			BodyHash:            s.BodyHash,
			NormalizedSignature: ns,
		}, true
	case source_live.SymbolKindMethod:
		ns := normalize.ForLanguage(s.LanguageID, s.Signature)
		id := MethodID(s.LanguageID, s.Receiver, s.Name, ns)
		return Entity{
			ID:                  id,
			Kind:                KindMethod,
			LanguageID:          s.LanguageID,
			QualifiedName:       s.QualifiedName,
			Receiver:            s.Receiver,
			Path:                s.Path,
			BodyHash:            s.BodyHash,
			NormalizedSignature: ns,
		}, true
	case source_live.SymbolKindTypeDecl:
		id := TypeDeclID(s.LanguageID, s.QualifiedName)
		return Entity{
			ID:            id,
			Kind:          KindTypeDecl,
			LanguageID:    s.LanguageID,
			QualifiedName: s.QualifiedName,
			Path:          s.Path,
			KindTag:       s.KindTag(),
		}, true
	case source_live.SymbolKindClass:
		id := TypeDeclID(s.LanguageID, s.QualifiedName)
		return Entity{
			ID:            id,
			Kind:          KindClass,
			LanguageID:    s.LanguageID,
			QualifiedName: s.QualifiedName,
			Path:          s.Path,
			KindTag:       s.KindTag(),
		}, true
	case source_live.SymbolKindInterface:
		id := TypeDeclID(s.LanguageID, s.QualifiedName)
		return Entity{
			ID:            id,
			Kind:          KindInterface,
			LanguageID:    s.LanguageID,
			QualifiedName: s.QualifiedName,
			Path:          s.Path,
			KindTag:       s.KindTag(),
		}, true
	case source_live.SymbolKindSymbol:
		// Catch-all — formula uses kind_tag as the discriminator.
		id := SymbolID(s.LanguageID, s.QualifiedName, s.KindTag())
		return Entity{
			ID:            id,
			Kind:          KindSymbol,
			LanguageID:    s.LanguageID,
			QualifiedName: s.QualifiedName,
			Path:          s.Path,
			KindTag:       s.KindTag(),
		}, true
	}
	return Entity{}, false
}

func entityKindFromSymbolKind(k source_live.SymbolKind) EntityKind {
	switch k {
	case source_live.SymbolKindFunction, source_live.SymbolKindClosure:
		return KindFunction
	case source_live.SymbolKindMethod:
		return KindMethod
	case source_live.SymbolKindTypeDecl:
		return KindTypeDecl
	case source_live.SymbolKindClass:
		return KindClass
	case source_live.SymbolKindInterface:
		return KindInterface
	}
	return KindSymbol
}

// freshnessForSourceClass maps a source's class to its default
// freshness when the extractor does not provide one explicitly. LSP
// is `live` (it observes the live editor buffer); SCIP is `current`
// (the index reflects committed state); tree-sitter is `live` (it
// re-parses on every edit). Per SPEC §4.6.
func freshnessForSourceClass(sc source_live.SourceClass) Freshness {
	switch sc {
	case source_live.SourceClassLSP:
		return FreshnessLive
	case source_live.SourceClassSCIP:
		return FreshnessCurrent
	case source_live.SourceClassTreesitter:
		return FreshnessLive
	}
	return FreshnessUnknown
}

func clampConfidence(c float64) float64 {
	switch {
	case c < 0:
		return 0
	case c > 1:
		return 1
	default:
		return c
	}
}
