// Package code_framework implements the code.framework layer (SPEC §2.3):
// framework / app enrichment over code.core. P2 activates the extractor
// plugin model and the v1 family set (routes, graphql, events, schemas,
// tests, generated, jobs, config). Extractors emit framework entities
// anchored to code.core entities via selectors (SPEC §2.2 —
// selectors-only cross-layer references); the Dispatcher caches
// resolutions as EntityRefs for hot-path performance and enforces
// compare-before-emit (§6.21) on every proposed write.
package code_framework

import (
	"context"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// EventKind is a fully-qualified event identifier of the form
// "<layer>.<Kind>", e.g. "code.core.FileChanged". Extractors subscribe
// across layer boundaries: a code.framework extractor may listen for
// source.live parse events and code.core drift events both, and the
// prefix is the disambiguator. The Dispatcher splits "<layer>.<Kind>"
// before passing it to the kernel EventFilter.
type EventKind string

// EntityKind is the unqualified entity kind an extractor emits into
// code.framework (e.g. "Route"). The same string MUST appear in
// schema.entity_kinds of internal/kernel/embedded_manifests/code.framework.yaml.
type EntityKind string

// Constants for the well-known input event kinds extractors observe.
// Not exhaustive — extractors may declare any qualified event kind in
// their Inputs(); these are the common ones with named constants for
// readability.
const (
	InputCoreFileChanged          EventKind = "code.core.FileChanged"
	InputCoreFileRemoved          EventKind = "code.core.FileRemoved"
	InputCoreEntityMaterialized   EventKind = "code.core.EntityMaterialized"
	InputCoreEntitySuperseded     EventKind = "code.core.EntitySuperseded"
	InputCoreSymbolAdded          EventKind = "code.core.SymbolAdded"
	InputCoreSymbolChanged        EventKind = "code.core.SymbolChanged"
	InputCoreSymbolRemoved        EventKind = "code.core.SymbolRemoved"
	InputCoreSymbolDisambiguation EventKind = "code.core.SymbolDisambiguation"
	InputLiveFileParsed           EventKind = "source.live.FileParsed"
)

// FallbackMode declares what an extractor does when its preferred input
// source (LSP / SCIP) is unavailable. Surfaced in Capabilities so the
// doctor command and CLI can advise the user.
type FallbackMode string

// Closed FallbackMode vocabulary.
const (
	FallbackTreesitterOnly FallbackMode = "treesitter_only"
	FallbackSkip           FallbackMode = "skip"
	FallbackLossy          FallbackMode = "lossy"
)

// BatchMode advises the Dispatcher whether to coalesce per-extractor
// inputs by transaction frame (SPEC §6.16) or deliver per-event.
type BatchMode string

// Closed BatchMode vocabulary.
const (
	BatchPerEvent BatchMode = "per_event"
	BatchPerTx    BatchMode = "per_tx"
)

// Capabilities is the closed v1 vocabulary for extractor self-description.
// Mirrors SPEC §5.3 in shape but scoped to extractors (not layers).
type Capabilities struct {
	// Family is one of: "routes", "graphql", "events", "schemas",
	// "tests", "generated", "config", "jobs". Used for CLI grouping
	// and enable/disable wildcards (e.g. --disable routes.*).
	Family string

	// Languages is the set of source_live language IDs this extractor
	// handles: subset of {"go", "typescript", "python"}.
	Languages []string

	// Frameworks names the libraries / patterns covered, e.g.
	// ["chi", "gorilla/mux", "net/http"]. Surfaced by
	// `graph-harness extractors status`; informational.
	Frameworks []string

	// Fallback declares the degradation strategy when the preferred
	// input source is unavailable.
	Fallback FallbackMode

	// BatchHint advises per-extractor delivery shape.
	BatchHint BatchMode
}

// Extractor is the Pass-1 contract for one framework family in one
// language (e.g. routes-go, events-py). One Go package per
// (family, language); each package's init() calls Register(name, ctor).
//
// Lifecycle: the Dispatcher owns Extractor instances. Per-instance state
// (per-extractor caches, parser scratch) is created by the constructor
// and reused across OnEvent invocations. OnEvent for distinct extractors
// runs concurrently; OnEvent for one extractor is serial per-event (the
// Dispatcher provides the synchronization).
type Extractor interface {
	// Name uniquely identifies the extractor instance, e.g.
	// "routes.go.chi" or "events.py.kafka". Used for enable/disable,
	// log lines, and Provenance.ProducedBy attribution.
	Name() string

	// Inputs returns the event kinds this extractor subscribes to.
	// Returned once at registration; the Dispatcher caches it.
	Inputs() []EventKind

	// Outputs returns the entity kinds this extractor may emit. Must
	// be a subset of code.framework's manifest entity_kinds. Used at
	// install time to validate manifest coverage and at runtime to
	// tag emitted events with the right Kind.
	Outputs() []EntityKind

	// Capabilities advertises framework-family coverage, language,
	// performance hints, and fallback strategy.
	Capabilities() Capabilities

	// OnEvent processes one input event. Returns proposed kernel
	// events to emit (one per state transition). Returning a nil
	// slice is the normal "nothing changed for this file" outcome
	// and is the suppress-at-source path (§6.21).
	//
	// The extractor MUST honor ctx.Done() within ~100 ms (SPEC §6.23).
	//
	// The returned events are NOT yet sequenced; the Dispatcher stamps
	// Seq + Tx (inheriting the input event's Tx for atomicity §6.16),
	// Layer ("code.framework"), ProducedBy
	// (SourceExtractorFramework + ":" + Name()), and Causes
	// ([]uint64{in.Seq}) before appending to the EventLog.
	OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error)
}
