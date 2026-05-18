package facts

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// EventLogFacts is the kernel-event-log adapter satisfying the SPEC §6.4
// Facts interface. The kernel event log itself is not a typical layer
// adapter (it owns the global seq + transport, not a per-layer query
// surface), but exposing it as a Facts implementation lets the rest of
// the system depend on a single uniform storage contract (F6 / P0.T08).
//
// One adapter per (EventLog, layer-scope) pair. Layer-scope is the
// layer name used to filter Snapshot / ReadCurrent / ReadAsOf; pass
// "" to scope across every layer.
//
// Compile-time assertion at the bottom of this file guarantees the
// adapter satisfies Facts.
type EventLogFacts struct {
	log   *EventLog
	layer string
}

// NewEventLogFacts wraps an *EventLog so it satisfies the Facts
// interface. `layer` filters reads/snapshots; pass "" for no filter.
func NewEventLogFacts(log *EventLog, layer string) *EventLogFacts {
	return &EventLogFacts{log: log, layer: layer}
}

// Write implements Facts.Write.
func (a *EventLogFacts) Write(ctx context.Context, events []kernel.Event) (uint64, error) {
	return a.log.Append(ctx, events)
}

// ReadCurrent implements Facts.ReadCurrent. The kernel log has no
// per-capability query DSL; this returns every committed event (or
// every event in the scoped layer) wrapped in a ResultEnvelope.
func (a *EventLogFacts) ReadCurrent(ctx context.Context, _ kernel.LayerQuery) (kernel.ResultEnvelope, error) {
	events, err := a.log.ReadAll(ctx)
	if err != nil {
		return kernel.ResultEnvelope{}, err
	}
	if a.layer != "" {
		events = filterByLayer(events, a.layer)
	}
	return wrapEnvelope(events, a.log.LastSeq())
}

// ReadAsOf implements Facts.ReadAsOf — pinned read at seq.
func (a *EventLogFacts) ReadAsOf(ctx context.Context, seq uint64, _ kernel.LayerQuery) (kernel.ResultEnvelope, error) {
	events, err := a.log.ReadAsOf(ctx, seq)
	if err != nil {
		return kernel.ResultEnvelope{}, err
	}
	if a.layer != "" {
		events = filterByLayer(events, a.layer)
	}
	return wrapEnvelope(events, seq)
}

// Snapshot implements Facts.Snapshot — delegates to CreateSnapshot
// with the adapter's layer scope.
func (a *EventLogFacts) Snapshot(ctx context.Context, seq uint64) (kernel.SnapshotHandle, error) {
	return a.log.CreateSnapshot(ctx, a.layer, seq)
}

// Restore implements Facts.Restore. Loads the snapshot's serialized
// events and re-appends them; idempotent at the kernel-event-log seam
// because event identity is content-addressable downstream and this
// path is only legitimate for cold-rebuild / chaos recovery (the
// daemon serves a single writer in steady state, so concurrent calls
// here are not a contract).
func (a *EventLogFacts) Restore(ctx context.Context, h kernel.SnapshotHandle) error {
	return a.log.Restore(ctx, h)
}

// Compact implements Facts.Compact — delegates to EventLog.Compact.
func (a *EventLogFacts) Compact(ctx context.Context, beforeSeq uint64, h kernel.SnapshotHandle) error {
	return a.log.Compact(ctx, beforeSeq, h)
}

// Subscribe implements Facts.Subscribe. The adapter ignores ctx (the
// stream's own Close releases resources); EventLog.SubscribeWithFilter
// never returns an error today, but the interface declares one for
// symmetry with future stream-construction failure modes.
func (a *EventLogFacts) Subscribe(_ context.Context, filter kernel.EventFilter) (EventStream, error) {
	return a.log.SubscribeWithFilter(filter), nil
}

func filterByLayer(events []kernel.Event, layer string) []kernel.Event {
	out := events[:0]
	for _, ev := range events {
		if ev.Layer == layer {
			out = append(out, ev)
		}
	}
	return out
}

func wrapEnvelope(events []kernel.Event, seq uint64) (kernel.ResultEnvelope, error) {
	data, err := json.Marshal(events)
	if err != nil {
		return kernel.ResultEnvelope{}, fmt.Errorf("marshal envelope: %w", err)
	}
	return kernel.ResultEnvelope{Data: data, ResolvedAtSeq: seq}, nil
}

// Compile-time assertion: the adapter satisfies Facts. Per F6 (the
// audit found Facts was dead code) this is the proof the contract is
// inhabited — future layer adapters can be checked against the same
// shape.
var _ Facts = (*EventLogFacts)(nil)
