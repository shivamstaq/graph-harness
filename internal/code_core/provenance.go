package code_core

import (
	"sort"

	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// SourceClass is the canonical wire/disk string for a fact source. The
// values mirror [source_live.SourceClass] so the unifier can plumb
// extractor-emitted Symbols straight into provenance rows without a
// translation table. Per SPEC §6.11 the three sources are:
//
//	live_lsp              — Language-Server-Protocol facts (gopls,
//	                        tsserver, pyright, …).
//	index_scip            — SCIP indexer output (committed-state precise
//	                        facts).
//	structural_treesitter — Tree-sitter structural parsing (always
//	                        available, type-unaware).
type SourceClass string

// Source-class constants. Kept in 1:1 alignment with
// [source_live.SourceClass] and the manifest declarations under
// `provenance_schema:`.
const (
	SourceClassLSP        SourceClass = "live_lsp"
	SourceClassSCIP       SourceClass = "index_scip"
	SourceClassTreesitter SourceClass = "structural_treesitter"
)

// Freshness is the per-source freshness class recorded alongside each
// SourceEntry. The values come from the source layer's freshness model
// (SPEC §4.6); code.core stores the source's claim verbatim and folds
// it into a layer-level freshness on read.
type Freshness string

// Standard freshness values. New values land via manifest extension —
// the kernel's [kernel.FreshnessClass] taxonomy is the union.
const (
	FreshnessLive          Freshness = "live"
	FreshnessCurrent       Freshness = "current"
	FreshnessPossiblyStale Freshness = "possibly_stale"
	FreshnessDirty         Freshness = "dirty"
	FreshnessStale         Freshness = "stale"
	FreshnessUnknown       Freshness = "unknown"
)

// SourceEntry is one fact source's claim on a code.core entity. The
// schema matches `code_entity_provenance` row-for-row; the type exists
// so the unifier and tests can manipulate provenance without going
// through SQL. Per SPEC §6.11 / §4.4:
//
//   - SourceClass — three-source unification key. Distinct values
//     across entries on the same entity mean "every source agrees this
//     entity exists with this canonical ID."
//   - Confidence — source's self-reported confidence, in [0, 1].
//   - LastSeenSeq — kernel event-log seq at which the source last
//     observed this entity. Drives freshness fold and supersession.
//   - Freshness — per-source freshness class at last observation.
//   - ProducedBy — human-readable producer identifier
//     ("extractor:lsp:gopls", "extractor:scip", "extractor:treesitter:go").
type SourceEntry struct {
	SourceClass SourceClass `json:"source_class"`
	Confidence  float64     `json:"confidence"`
	LastSeenSeq uint64      `json:"last_seen_seq"`
	Freshness   Freshness   `json:"freshness"`
	ProducedBy  string      `json:"produced_by,omitempty"`
}

// Provenance is the merged provenance record for a single entity — the
// in-memory analogue of every `code_entity_provenance` row keyed on
// the entity's ID. The fold (SPEC §4.5) computes a summary; the raw
// SourceEntry list is kept so downstream consumers (Studio's
// per-source drill-down, change.process diff resolution) can show
// individual claims.
type Provenance struct {
	Sources []SourceEntry `json:"sources"`
}

// Merge inserts e into p, replacing any existing entry with the same
// SourceClass when the new entry is fresher (higher LastSeenSeq) or
// more confident (when seqs are equal). Order in the resulting slice
// is canonicalized by SourceClass so two equivalent provenance sets
// compare byte-equal.
func (p *Provenance) Merge(e SourceEntry) {
	for i, existing := range p.Sources {
		if existing.SourceClass != e.SourceClass {
			continue
		}
		switch {
		case e.LastSeenSeq > existing.LastSeenSeq:
			p.Sources[i] = e
		case e.LastSeenSeq == existing.LastSeenSeq && e.Confidence > existing.Confidence:
			p.Sources[i] = e
		}
		p.canonicalize()
		return
	}
	p.Sources = append(p.Sources, e)
	p.canonicalize()
}

// HasSource reports whether p contains an entry from sc.
func (p Provenance) HasSource(sc SourceClass) bool {
	for _, e := range p.Sources {
		if e.SourceClass == sc {
			return true
		}
	}
	return false
}

// SourceClasses returns the distinct source-class values currently
// recorded, in canonical order. Useful in tests asserting "this entity
// has agreement from all three sources."
func (p Provenance) SourceClasses() []SourceClass {
	out := make([]SourceClass, 0, len(p.Sources))
	for _, e := range p.Sources {
		out = append(out, e.SourceClass)
	}
	return out
}

func (p *Provenance) canonicalize() {
	sort.Slice(p.Sources, func(i, j int) bool {
		return p.Sources[i].SourceClass < p.Sources[j].SourceClass
	})
}

// SourceClassFromSymbol maps the source_live envelope's source class
// to the code.core string used in the provenance table. Returns the
// empty SourceClass when the input is unrecognized — callers should
// treat that as a programmer error and skip the entry rather than
// silently bucketing into a default class.
func SourceClassFromSymbol(s source_live.SourceClass) SourceClass {
	switch s {
	case source_live.SourceClassLSP:
		return SourceClassLSP
	case source_live.SourceClassSCIP:
		return SourceClassSCIP
	case source_live.SourceClassTreesitter:
		return SourceClassTreesitter
	}
	return ""
}
