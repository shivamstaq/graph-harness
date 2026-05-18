package facts

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// BenchmarkEventLogAppend measures append throughput. Run with
//
//	go test -bench BenchmarkEventLogAppend ./internal/facts/
//
// Reports events per second alongside Go's default ns/op. Batches
// of 100 events approximate the daemon's typical commit shape
// (watcher coalescing flushes ~tens-to-hundreds of events per
// re-extract).
//
// F5 / P0 Gate 11: ≥50K events/sec under steady-state load.
func BenchmarkEventLogAppend(b *testing.B) {
	tmp := b.TempDir()
	log, err := OpenEventLog(filepath.Join(tmp, "bench.db"))
	if err != nil {
		b.Fatalf("OpenEventLog: %v", err)
	}
	b.Cleanup(func() { _ = log.Close() })

	const batchSize = 100
	batch := make([]kernel.Event, batchSize)
	for i := range batch {
		batch[i] = kernel.Event{
			Layer:      "code.core",
			Kind:       "FileChanged",
			Payload:    json.RawMessage(`{"path":"a.go"}`),
			ProducedBy: kernel.SourceLayerInternal,
		}
	}

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := log.Append(ctx, batch); err != nil {
			b.Fatalf("Append: %v", err)
		}
	}
	b.StopTimer()
	totalEvents := int64(b.N) * int64(batchSize)
	if b.Elapsed() > 0 {
		eps := float64(totalEvents) / b.Elapsed().Seconds()
		b.ReportMetric(eps, "events/sec")
	}
}

// TestEventLogThroughputFloor is the gate: appends events for 2s and
// fails if the sustained rate is < 50K events/sec. Skipped under
// `-short` so CI's full-suite run can opt in selectively.
//
// F5 / P0 Gate 11.
func TestEventLogThroughputFloor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping throughput floor test in short mode")
	}
	tmp := t.TempDir()
	log, err := OpenEventLog(filepath.Join(tmp, "throughput.db"))
	if err != nil {
		t.Fatalf("OpenEventLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	const (
		batchSize = 100
		floor     = 50_000.0 // events/sec
		window    = 2 * time.Second
	)
	batch := make([]kernel.Event, batchSize)
	for i := range batch {
		batch[i] = kernel.Event{
			Layer:      "code.core",
			Kind:       "FileChanged",
			Payload:    json.RawMessage(`{"path":"a.go"}`),
			ProducedBy: kernel.SourceLayerInternal,
		}
	}

	ctx := context.Background()
	deadline := time.Now().Add(window)
	var total int64
	for time.Now().Before(deadline) {
		if _, err := log.Append(ctx, batch); err != nil {
			t.Fatalf("Append: %v", err)
		}
		total += int64(batchSize)
	}
	eps := float64(total) / window.Seconds()
	t.Logf("throughput: %.0f events/sec over %s (total %d)", eps, window, total)
	if eps < floor {
		t.Fatalf("throughput %.0f events/sec below floor %.0f (P0 Gate 11)", eps, floor)
	}
}
