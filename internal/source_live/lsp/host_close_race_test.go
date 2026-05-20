package lsp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// raceDriver is a Driver fake whose Initialize blocks until a release
// channel is closed. It lets the test interleave Host.Close between
// the moment Initialize returns and DriverFor's final map assignment.
type raceDriver struct {
	release  <-chan struct{}
	shutdown chan struct{}

	mu       sync.Mutex
	shutdone bool
}

func (d *raceDriver) Language() string { return "fake" }

func (d *raceDriver) Initialize(ctx context.Context, _ string) error {
	select {
	case <-d.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *raceDriver) DocumentSymbol(_ context.Context, _ string) ([]source_live.Symbol, error) {
	return nil, nil
}

func (d *raceDriver) Definition(_ context.Context, _ string, _, _ uint32) ([]Location, error) {
	return nil, nil
}

func (d *raceDriver) References(_ context.Context, _ string, _, _ uint32, _ bool) ([]Location, error) {
	return nil, nil
}

func (d *raceDriver) Health(_ context.Context) error               { return nil }
func (d *raceDriver) SetNotificationHandler(_ NotificationHandler) {}

func (d *raceDriver) Shutdown(_ context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.shutdone {
		d.shutdone = true
		close(d.shutdown)
	}
	return nil
}

// TestHost_CloseDuringInitialize_NoPanic guards against the F21 race
// (see plan/phase-0-thru-1.5-closure.md): if Host.Close runs while a
// DriverFor invocation is blocked in d.Initialize, the bare
// `h.drivers[languageID] = d` assignment that followed would hit a
// nil map and panic. The fix re-checks h.closed under the lock and
// shuts the orphaned driver down instead.
func TestHost_CloseDuringInitialize_NoPanic(t *testing.T) {
	release := make(chan struct{})
	drv := &raceDriver{release: release, shutdown: make(chan struct{}, 1)}

	reg := NewRegistry()
	// "sh" is on every POSIX $PATH; LookupExecutable just needs the
	// binary to exist — it does not actually exec it.
	reg.Register("fake", "sh", func() Driver { return drv })

	h := NewHost(reg, "/tmp")

	driverForResult := make(chan error, 1)
	go func() {
		_, err := h.DriverFor(context.Background(), "fake")
		driverForResult <- err
	}()

	// Give the DriverFor goroutine time to enter Initialize.
	time.Sleep(50 * time.Millisecond)

	// Close synchronously while Initialize is still parked. The driver
	// has not yet been recorded in h.drivers (Initialize hasn't
	// returned), so Close has nothing to Shutdown and returns
	// near-instantly. Critically, by the time Close returns,
	// h.closed=true and h.drivers=nil are visible to DriverFor's
	// next lock acquisition.
	closeDone := make(chan struct{})
	go func() {
		_ = h.Close(context.Background())
		close(closeDone)
	}()
	<-closeDone

	// Now unblock Initialize. DriverFor re-takes h.mu, sees closed=true,
	// Shutdown-s the orphaned driver, and returns a host-closed error.
	close(release)

	select {
	case err := <-driverForResult:
		if err == nil {
			t.Fatal("DriverFor returned a Driver after Close; expected host-closed error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DriverFor did not return within 2s after Close+release")
	}

	select {
	case <-drv.shutdown:
	case <-time.After(time.Second):
		t.Fatal("orphaned driver was not Shutdown after host close")
	}
}
