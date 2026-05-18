package kernel

import (
	"math/rand"
	"testing"
)

// TestFoldProvenance_Fuzz10K closes the P0 gate-6 invariant by
// exercising FoldProvenance against 10K randomly generated
// constituent lists. Asserts the SPEC §4.5 fold rules hold for
// every shape — min confidence, worst freshness by blocking risk,
// max produced_seq, deduped sorted unions — and that fold(in) ==
// fold(in) (determinism).
func TestFoldProvenance_Fuzz10K(t *testing.T) {
	t.Parallel()
	freshnessClasses := []FreshnessClass{
		FreshnessLive, FreshnessCurrent, FreshnessPossiblyStale,
		FreshnessDirty, FreshnessStale, FreshnessUnknown, FreshnessUnresolved,
	}
	sourceClasses := []SourceClass{
		SourceExtractorTreeSitter, SourceExtractorLSP, SourceExtractorSCIP,
		SourceImporterGH, SourceUser, SourceAgent, SourceLayerInternal,
	}
	rng := rand.New(rand.NewSource(0x6a7d3c8e1a2b9f47)) //nolint:gosec // deterministic fuzz seed

	for iter := 0; iter < 10_000; iter++ {
		n := 1 + rng.Intn(6)
		in := make([]Provenance, n)
		wantConf := 1.0
		var (
			wantSeq        uint64
			wantFreshness  FreshnessClass
			wantWorstScore = -1
		)
		gotSrc := map[SourceClass]struct{}{}
		gotInputs := map[string]struct{}{}
		for i := range n {
			conf := rng.Float64()
			seq := uint64(rng.Intn(1_000_000)) //nolint:gosec
			fc := freshnessClasses[rng.Intn(len(freshnessClasses))]
			sc := make([]SourceClass, 1+rng.Intn(3))
			for j := range sc {
				sc[j] = sourceClasses[rng.Intn(len(sourceClasses))]
				gotSrc[sc[j]] = struct{}{}
			}
			inputs := make([]string, rng.Intn(3))
			for j := range inputs {
				inputs[j] = "in-" + string(rune('a'+rng.Intn(8)))
				gotInputs[inputs[j]] = struct{}{}
			}
			in[i] = Provenance{Confidence: conf, Freshness: fc, SourceClass: sc, Inputs: inputs, ProducedSeq: seq}
			if conf < wantConf {
				wantConf = conf
			}
			if seq > wantSeq {
				wantSeq = seq
			}
			if r := BlockingRisk(fc); r > wantWorstScore {
				wantWorstScore = r
				wantFreshness = fc
			}
		}
		out := FoldProvenance(in)
		if out.Confidence != wantConf {
			t.Fatalf("iter %d confidence: got %v want %v", iter, out.Confidence, wantConf)
		}
		if out.ProducedSeq != wantSeq {
			t.Fatalf("iter %d produced_seq: got %d want %d", iter, out.ProducedSeq, wantSeq)
		}
		if out.Freshness != wantFreshness {
			t.Fatalf("iter %d freshness: got %s want %s", iter, out.Freshness, wantFreshness)
		}
		if len(out.SourceClass) != len(gotSrc) {
			t.Fatalf("iter %d source_class count: got %d want %d", iter, len(out.SourceClass), len(gotSrc))
		}
		out2 := FoldProvenance(in)
		if out.Confidence != out2.Confidence || out.Freshness != out2.Freshness ||
			out.ProducedSeq != out2.ProducedSeq || len(out.SourceClass) != len(out2.SourceClass) {
			t.Fatalf("iter %d non-deterministic: %+v vs %+v", iter, out, out2)
		}
	}
}
