package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// fakeReader is a per-test LayerReader stub that records the args it
// received and returns the configured ResultEnvelope.
type fakeReader struct {
	currentEnv   ResultEnvelope
	asOfEnv      ResultEnvelope
	currentErr   error
	asOfErr      error
	lastCapaCurr string
	lastCapaAsOf string
	lastAsOf     uint64
}

func (f *fakeReader) ReadCurrent(_ context.Context, q LayerQuery) (ResultEnvelope, error) {
	f.lastCapaCurr = q.Capability
	return f.currentEnv, f.currentErr
}

func (f *fakeReader) ReadAsOf(_ context.Context, seq uint64, q LayerQuery) (ResultEnvelope, error) {
	f.lastAsOf = seq
	f.lastCapaAsOf = q.Capability
	return f.asOfEnv, f.asOfErr
}

func TestRouter_RouteCurrentPinsToHead(t *testing.T) {
	r := NewRouter(func() uint64 { return 42 })
	reader := &fakeReader{currentEnv: ResultEnvelope{Data: json.RawMessage(`{"x":1}`)}}
	r.Register("code.core", reader)

	got, err := r.Route(context.Background(), RouteRequest{
		Layer:      "code.core",
		Capability: "lookup_entity",
		Args:       json.RawMessage(`{"id":"e1"}`),
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got.ResolvedAtSeq != 42 {
		t.Errorf("resolved_at_seq: want 42, got %d", got.ResolvedAtSeq)
	}
	if reader.lastCapaCurr != "lookup_entity" {
		t.Errorf("capability not forwarded; got %q", reader.lastCapaCurr)
	}
}

func TestRouter_RouteAsOfStampsHistoricalSeq(t *testing.T) {
	r := NewRouter(func() uint64 { return 200 })
	reader := &fakeReader{asOfEnv: ResultEnvelope{Data: json.RawMessage(`{"x":1}`)}}
	r.Register("code.core", reader)

	got, err := r.Route(context.Background(), RouteRequest{
		Layer:      "code.core",
		Capability: "lookup_entity",
		AsOfSeq:    100,
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got.ResolvedAtSeq != 100 {
		t.Errorf("resolved_at_seq: want 100, got %d", got.ResolvedAtSeq)
	}
	if reader.lastAsOf != 100 {
		t.Errorf("reader didn't receive as-of seq; got %d", reader.lastAsOf)
	}
}

func TestRouter_MalformedQueryRejected(t *testing.T) {
	r := NewRouter(nil)
	_, err := r.Route(context.Background(), RouteRequest{Capability: "lookup"})
	if !errors.Is(err, ErrMalformedQuery) {
		t.Errorf("missing layer: want ErrMalformedQuery, got %v", err)
	}
	_, err = r.Route(context.Background(), RouteRequest{Layer: "code.core"})
	if !errors.Is(err, ErrMalformedQuery) {
		t.Errorf("missing capability: want ErrMalformedQuery, got %v", err)
	}
}

func TestRouter_UnregisteredLayer(t *testing.T) {
	r := NewRouter(nil)
	_, err := r.Route(context.Background(), RouteRequest{Layer: "code.core", Capability: "lookup"})
	if !errors.Is(err, ErrLayerNotFound) {
		t.Errorf("want ErrLayerNotFound, got %v", err)
	}
}

func TestRouter_AdapterPreservesEnvelopeSeq(t *testing.T) {
	// When the adapter pre-stamps ResolvedAtSeq the router preserves it
	// rather than overwriting with the pinned head — adapters may have
	// pinned at an internal snapshot earlier than the router-sampled head.
	r := NewRouter(func() uint64 { return 42 })
	r.Register("code.core", &fakeReader{currentEnv: ResultEnvelope{ResolvedAtSeq: 30}})
	got, _ := r.Route(context.Background(), RouteRequest{Layer: "code.core", Capability: "lookup"})
	if got.ResolvedAtSeq != 30 {
		t.Errorf("pre-stamped seq overwritten: got %d", got.ResolvedAtSeq)
	}
}

func TestCompose_FoldsProvenance(t *testing.T) {
	envs := map[string]ResultEnvelope{
		"code.core": {
			Data:          json.RawMessage(`{"entities":1}`),
			ResolvedAtSeq: 50,
			Provenance: Provenance{
				Confidence: 0.95, Freshness: FreshnessCurrent,
				SourceClass: []SourceClass{SourceExtractorLSP},
				ProducedSeq: 50,
			},
		},
		"semantic.overlay": {
			Data:          json.RawMessage(`{"selectors":1}`),
			ResolvedAtSeq: 50,
			Provenance: Provenance{
				Confidence: 0.80, Freshness: FreshnessPossiblyStale,
				SourceClass: []SourceClass{SourceImporterGH},
				ProducedSeq: 40,
			},
		},
	}
	composed := Compose(envs)
	if composed.ResolvedAtSeq != 50 {
		t.Errorf("composed resolved_at_seq: want 50, got %d", composed.ResolvedAtSeq)
	}
	if composed.Provenance.Confidence != 0.80 {
		t.Errorf("provenance fold min: want 0.80, got %v", composed.Provenance.Confidence)
	}
	if composed.Provenance.Freshness != FreshnessPossiblyStale {
		t.Errorf("provenance fold worst-freshness: want possibly_stale, got %s", composed.Provenance.Freshness)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(composed.Data, &got); err != nil {
		t.Fatalf("composed data not a JSON object: %v", err)
	}
	if _, ok := got["code.core"]; !ok {
		t.Errorf("composed data missing code.core key")
	}
	if _, ok := got["semantic.overlay"]; !ok {
		t.Errorf("composed data missing semantic.overlay key")
	}
}

func TestCompose_EmptyEnvelopes(t *testing.T) {
	out := Compose(nil)
	if out.ResolvedAtSeq != 0 || out.Data != nil {
		t.Errorf("empty compose: want zero envelope, got %+v", out)
	}
}
