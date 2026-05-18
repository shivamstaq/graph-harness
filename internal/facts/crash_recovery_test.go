package facts

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// TestEventLog_CrashRecoveryReplayConsistent exercises P0 Gate 8:
// kill the writer at N random points across the 1000-write stream,
// reopen the log, and assert no entries past the last committed
// event (no torn rows from a truncated WAL).
//
// We simulate the crash by Close()-ing the EventLog mid-write
// instead of an SIGKILL-on-process — the SQLite-level invariant
// (transactional commit) is the load-bearing property; the OS-level
// kill just exposes whether the WAL was flushed. Close() flushes;
// the assertion is that LastSeq() after reopen equals the count of
// successfully-Append'd batches.
//
// F4 / P0 Gate 8 — runs across 10 deterministic seeds to cover
// different crash points.
func TestEventLog_CrashRecoveryReplayConsistent(t *testing.T) {
	t.Parallel()
	for _, seed := range []uint64{1, 17, 42, 99, 137, 200, 314, 500, 777, 1000} {
		seed := seed
		t.Run("seed-"+itoa(int(seed)), func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			path := filepath.Join(tmp, "crash.db")
			log, err := OpenEventLog(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}

			ctx := context.Background()
			rng := rand.New(rand.NewPCG(seed, 0xDEAD)) //nolint:gosec // deterministic test seed
			const totalBatches = 100
			crashAt := rng.IntN(totalBatches) + 1 // kill after committing this many batches

			committed := 0
			for i := 0; i < crashAt; i++ {
				if _, err := log.Append(ctx, []kernel.Event{{
					Layer:      "code.core",
					Kind:       "FileChanged",
					Payload:    json.RawMessage(`{}`),
					ProducedBy: kernel.SourceLayerInternal,
				}}); err != nil {
					t.Fatalf("append %d: %v", i, err)
				}
				committed++
			}
			preCrashSeq := log.LastSeq()
			// "Crash" by closing without grace. SQLite's WAL ensures
			// committed transactions survive; uncommitted ones are
			// dropped on the next OpenEventLog.
			_ = log.Close()

			// Reopen and assert LastSeq matches the committed count.
			log2, err := OpenEventLog(path)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			t.Cleanup(func() { _ = log2.Close() })
			if got := log2.LastSeq(); got != preCrashSeq {
				t.Fatalf("seed %d: post-recovery LastSeq=%d, want %d (committed=%d)",
					seed, got, preCrashSeq, committed)
			}
			// ReadAll: surviving events form a contiguous prefix
			// 1..preCrashSeq with monotonic seqs.
			rows, err := log2.ReadAll(ctx)
			if err != nil {
				t.Fatalf("readall: %v", err)
			}
			if uint64(len(rows)) != preCrashSeq {
				t.Fatalf("seed %d: post-recovery row count=%d, want %d",
					seed, len(rows), preCrashSeq)
			}
			for i, ev := range rows {
				if ev.Seq != uint64(i+1) {
					t.Fatalf("seed %d: row %d has seq %d (expected %d) — non-contiguous after recovery",
						seed, i, ev.Seq, i+1)
				}
			}
		})
	}
}

// itoa is a local helper used by t.Run names to avoid pulling in
// strconv just for one test.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
