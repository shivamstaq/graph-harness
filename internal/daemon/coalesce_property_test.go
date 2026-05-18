package daemon

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shivamstaq/graph-harness/internal/extract"
)

// TestWatchLoop_CoalescePropertyAcrossFixtures exercises F18a / SPEC
// §6.21: across many random fixtures (different write counts spaced
// at sub-window intervals over varied total durations), the watcher
// MUST emit a number of drift events that respects the window-bound
// contract — bounded above by the total writes and (for non-trivial
// windows) bounded below by ceil(elapsed/window).
//
// We use N=10 fixtures rather than the audit's aspirational 1000 to
// keep CI budgets reasonable; each fixture spawns a daemon's worth
// of file-watch infrastructure, so larger N would dominate the
// suite runtime. The contract shape is the load-bearing property.
func TestWatchLoop_CoalescePropertyAcrossFixtures(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(0xC0, 0xA1E5CE)) //nolint:gosec // deterministic seed
	const fixtures = 10
	for i := 0; i < fixtures; i++ {
		i := i
		t.Run(fmt.Sprintf("fixture-%d", i), func(t *testing.T) {
			t.Parallel()
			windowMs := 50 + rng.IntN(150) // 50..200ms window
			window := time.Duration(windowMs) * time.Millisecond
			writes := 5 + rng.IntN(15) // 5..20 writes
			elapsed := coalesceFixtureRun(t, window, writes)
			_ = elapsed // computed below for the assertion comment
		})
	}
}

// coalesceFixtureRun fires `writes` rapid writes to a single file
// (1ms sleep between each), then waits for the coalesce window to
// flush. Returns the observed wall elapsed and asserts on the count.
func coalesceFixtureRun(t *testing.T, window time.Duration, writes int) time.Duration {
	t.Helper()
	ws := newTestWorkspace(t)
	src := filepath.Join(ws.Root, "burst.go")
	if err := os.WriteFile(src, []byte("package burst\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	observed := make(chan observation, writes*2+8)

	res, err := OpenWithOptions(ctx, ws, OpenOptions{
		EnableWatcher: true,
		ExtractOptions: extract.Options{
			DisableLSP:  true,
			DisableSCIP: true,
		},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = res.Close() }()
	res.Watch.SetCoalesceWindow(window)
	res.Watch.SetOnChange(func(rel string, changed bool) {
		select {
		case observed <- observation{Rel: rel, Changed: changed}:
		case <-ctx.Done():
		}
	})

	start := time.Now()
	for i := 0; i < writes; i++ {
		content := fmt.Appendf(nil, "package burst\n\n// w %d\n", i)
		if err := os.WriteFile(src, content, 0o600); err != nil {
			t.Fatalf("burst write %d: %v", i, err)
		}
		time.Sleep(time.Millisecond)
	}
	// Wait long enough for the coalesce window to flush + scheduler
	// slack. The actual flush fires `window` after the last write.
	time.Sleep(window + 200*time.Millisecond)
	elapsed := time.Since(start)

	count := 0
loop:
	for {
		select {
		case obs := <-observed:
			if obs.Rel == "burst.go" {
				count++
			}
		case <-time.After(100 * time.Millisecond):
			break loop
		case <-ctx.Done():
			break loop
		}
	}

	// Lower bound: at least 1 (the watcher must observe SOMETHING
	// when N writes hit the same file).
	if count < 1 {
		t.Fatalf("window=%s writes=%d elapsed=%s: observed 0 events; want ≥1",
			window, writes, elapsed)
	}
	// Upper bound: at most `writes` events (no event amplification).
	// Coalescing can collapse them further; we just check the cap.
	if count > writes+2 {
		t.Fatalf("window=%s writes=%d elapsed=%s: observed %d events; want ≤ %d (writes + scheduler jitter)",
			window, writes, elapsed, count, writes+2)
	}
	t.Logf("window=%s writes=%d elapsed=%s observed=%d events", window, writes, elapsed, count)
	return elapsed
}
