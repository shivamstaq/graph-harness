package code_core

import (
	"context"
	"errors"
	"fmt"
)

// ErrEntityNotFound is returned by [LookupEntity] / [LookupProvenance]
// when the requested entity ID does not exist in the store. Callers
// rendering Studio drill-down or MCP `gh://entity/...` resources can
// distinguish missing entities from query errors via this sentinel.
var ErrEntityNotFound = errors.New("code.core: entity not found")

// EntityView is the consumer-facing shape for a single code.core
// entity plus its merged provenance. It is the canonical input to:
//
//   - Studio's per-entity drill-down panel (entity attributes on the
//     left, provenance.sources[] table on the right);
//   - the MCP `gh://entity/code.core/<kind>/<id>` resource (one
//     EntityView marshalled to JSON);
//   - any change.process / review.queue surface that wants to show
//     "where did this fact come from?" without re-issuing SQL.
//
// Fields:
//
//   - Entity: the entity row verbatim (content-addressable ID, kind,
//     language, qualified name, receiver, path, body hash, kind_tag,
//     parent_id, ordinal, normalized_signature, fingerprints).
//   - Provenance: the full ProvenanceView for the entity (folded
//     summary + ordered list of per-source claims).
//
// The view is a pure value: snapshotted at lookup time. Callers that
// need a streaming subscription should subscribe to `code.core` events
// directly via the kernel.
type EntityView struct {
	Entity     Entity         `json:"entity"`
	Provenance ProvenanceView `json:"provenance"`
}

// ProvenanceView wraps the per-source claim list with a folded
// summary. Per SPEC §4.5 the fold collapses constituent provenance
// into a single record so consumers can render a one-liner
// ("3 sources agree, current freshness, confidence 0.92") without
// reasoning about every source individually.
//
// Sources is the canonical-ordered (by SourceClass) list of
// individual claims; Summary is the fold result.
type ProvenanceView struct {
	Summary ProvenanceSummary `json:"summary"`
	Sources []SourceEntry     `json:"sources"`
}

// ProvenanceSummary is the §4.5 fold output specialized for
// code.core's per-source provenance shape. Field semantics:
//
//   - SourceCount      — number of distinct fact sources backing the
//     entity. 1, 2, or 3 in v1 (LSP / SCIP /
//     tree-sitter); the disagreement_threshold
//     gate emits SymbolDisambiguation when the
//     same logical location accumulates ≥ N
//     distinct canonical IDs, so SourceCount on a
//     single EntityView is implicitly capped at the
//     number of sources voting for *this* ID.
//   - Confidence       — `min` over per-source confidence (the SPEC
//     §4.5 rule: a single low-confidence source
//     reduces the aggregate).
//   - Freshness        — `worst` over per-source freshness, ordered
//     by [kernel.BlockingRisk]: stale beats dirty
//     beats current beats live. The folded
//     freshness is what surfaces should display
//     when summarizing the entity in one line.
//   - LatestSeenSeq    — `max` over LastSeenSeq across sources.
//     Drives "this fact has not been re-observed
//     since seq X" warnings.
//   - SourceClasses    — sorted union of source-class IDs present in
//     Sources. Useful for "agreed by LSP+SCIP+ts"
//     chip rendering.
type ProvenanceSummary struct {
	SourceCount   int           `json:"source_count"`
	Confidence    float64       `json:"confidence"`
	Freshness     Freshness     `json:"freshness"`
	LatestSeenSeq uint64        `json:"latest_seen_seq"`
	SourceClasses []SourceClass `json:"source_classes"`
}

// LookupEntity returns the EntityView for the given ID. Returns
// [ErrEntityNotFound] when the entity is unknown so callers can
// distinguish "missing" from "transport error" without inspecting
// nil pointers. The provenance list is sorted in canonical
// (SourceClass-ascending) order so two equivalent EntityViews compare
// byte-equal once marshalled to JSON.
func (s *Store) LookupEntity(ctx context.Context, id string) (EntityView, error) {
	ent, err := s.LookupEntityByID(ctx, id)
	if err != nil {
		return EntityView{}, fmt.Errorf("lookup entity %s: %w", id, err)
	}
	if ent == nil {
		return EntityView{}, ErrEntityNotFound
	}
	pv, err := s.lookupProvenanceView(ctx, id)
	if err != nil {
		return EntityView{}, err
	}
	return EntityView{Entity: *ent, Provenance: pv}, nil
}

// LookupProvenance returns just the ProvenanceView for an entity.
// Equivalent to `LookupEntity(ctx, id).Provenance` minus the entity
// fetch, so use this when the caller already has the Entity in hand
// (e.g. during selector binding render). Returns [ErrEntityNotFound]
// when the entity does not exist; an entity with no recorded
// provenance returns a zero-source ProvenanceView with no error.
func (s *Store) LookupProvenance(ctx context.Context, id string) (ProvenanceView, error) {
	// Existence check: if the entity is missing, surface
	// ErrEntityNotFound rather than silently returning a zero view.
	ent, err := s.LookupEntityByID(ctx, id)
	if err != nil {
		return ProvenanceView{}, fmt.Errorf("lookup entity %s: %w", id, err)
	}
	if ent == nil {
		return ProvenanceView{}, ErrEntityNotFound
	}
	return s.lookupProvenanceView(ctx, id)
}

func (s *Store) lookupProvenanceView(ctx context.Context, id string) (ProvenanceView, error) {
	entries, err := s.GetProvenance(ctx, id)
	if err != nil {
		return ProvenanceView{}, fmt.Errorf("get provenance %s: %w", id, err)
	}
	return ProvenanceView{
		Summary: foldProvenance(entries),
		Sources: entries,
	}, nil
}

// foldProvenance applies the SPEC §4.5 fold rules to a per-source
// provenance list. The kernel ships a fold for the broader
// [kernel.Provenance] shape; code.core uses a narrower input
// ([SourceEntry]) so this helper specializes the rules without
// dragging in the kernel package.
//
// Rules:
//
//	confidence       = min(per-source confidence)
//	freshness        = worst(per-source freshness, by BlockingRisk)
//	latest_seen_seq  = max(per-source last_seen_seq)
//	source_classes   = sorted union (already canonical via canonicalize)
//	source_count     = len(entries)
func foldProvenance(entries []SourceEntry) ProvenanceSummary {
	if len(entries) == 0 {
		return ProvenanceSummary{}
	}
	out := ProvenanceSummary{
		SourceCount:   len(entries),
		Confidence:    entries[0].Confidence,
		Freshness:     entries[0].Freshness,
		LatestSeenSeq: entries[0].LastSeenSeq,
		SourceClasses: make([]SourceClass, 0, len(entries)),
	}
	worstRisk := freshnessRisk(entries[0].Freshness)
	for i, e := range entries {
		if i > 0 && e.Confidence < out.Confidence {
			out.Confidence = e.Confidence
		}
		if r := freshnessRisk(e.Freshness); r > worstRisk {
			worstRisk = r
			out.Freshness = e.Freshness
		}
		if e.LastSeenSeq > out.LatestSeenSeq {
			out.LatestSeenSeq = e.LastSeenSeq
		}
		out.SourceClasses = append(out.SourceClasses, e.SourceClass)
	}
	// SourceClasses arrives canonicalized from GetProvenance (ORDER BY
	// source_class ASC), so no resort is required.
	return out
}

// freshnessRisk maps per-source freshness to the same blocking-risk
// score the kernel uses for [kernel.FreshnessClass]. Higher = more
// stale. Mirroring the kernel's order keeps code.core's freshness
// fold compatible with cross-layer fold logic without taking a hard
// dependency on the kernel type.
func freshnessRisk(f Freshness) int {
	switch f {
	case FreshnessLive:
		return 0
	case FreshnessCurrent:
		return 1
	case FreshnessPossiblyStale:
		return 2
	case FreshnessDirty:
		return 3
	case FreshnessStale:
		return 4
	}
	return 5 // FreshnessUnknown / unrecognized — most cautious bucket.
}
