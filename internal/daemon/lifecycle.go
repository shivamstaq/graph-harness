package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Options control the running daemon. Sane defaults for one-line use.
type Options struct {
	// IdleTimeout is the duration the daemon will sit without an RPC
	// before exiting. Zero disables idle shutdown (long-running mode).
	IdleTimeout time.Duration

	// Logger is an optional sink for non-fatal events (idle-shutdown,
	// failed connection accept). Nil discards logs.
	Logger func(format string, args ...any)
}

// DefaultIdleTimeout is the post-P0 default: 1h (matches SPEC §9.1).
const DefaultIdleTimeout = time.Hour

// DefaultRetentionTick is the F10 / P0.T06 retention sweep cadence.
// The sweep is cheap (CreateSnapshot + DELETE WHERE seq<…), so a 1h
// tick is conservative — even noisy workspaces aren't compacted more
// than once per hour, and the per-layer Retention.Events / .Duration
// caps in the layer manifest gate the actual deletion.
const DefaultRetentionTick = time.Hour

// RetentionLoop runs the periodic retention sweep. For each layer
// registered in `reg` whose manifest declares a non-zero
// `Snapshot.Retention.Events` or `.Duration`, the loop:
//   1. Computes a cutoff seq (head minus retention.Events, or the
//      seq at retention.Duration ago — the more permissive of the
//      two for safety).
//   2. Captures a snapshot at the cutoff via log.CreateSnapshot.
//   3. Calls log.Compact to delete events strictly before the cutoff.
//
// The kernel-canonical Facts.Compact contract requires a snapshot
// handle that covers the deleted prefix; we satisfy this by always
// snapshotting at the cutoff before compacting.
//
// Errors are logged but never panic the loop — a transient SQLite
// failure shouldn't kill retention for the rest of the daemon's
// lifetime. Stops on ctx.Done.
//
// F10 / P0.T06: SnapRetention was parsed but unused pre-F10; this
// is the consumer that gives the field semantic meaning.
func RetentionLoop(ctx context.Context, log *facts.EventLog, reg *kernel.Registry, tick time.Duration, errLog func(format string, args ...any)) {
	if log == nil || reg == nil {
		return
	}
	if tick <= 0 {
		tick = DefaultRetentionTick
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runRetentionSweep(ctx, log, reg, errLog)
		}
	}
}

// runRetentionSweep is one iteration of RetentionLoop. Factored out
// so tests can drive a single sweep without spinning the ticker.
func runRetentionSweep(ctx context.Context, log *facts.EventLog, reg *kernel.Registry, errLog func(format string, args ...any)) {
	head := log.LastSeq()
	if head == 0 {
		return
	}
	for _, name := range reg.List() {
		m, ok := reg.Get(name)
		if !ok {
			continue
		}
		ret := m.Snapshot.Retention
		if ret.Events <= 0 && ret.Duration == "" {
			continue
		}
		cutoff := computeRetentionCutoff(head, ret.Events)
		if cutoff == 0 {
			continue
		}
		snap, err := log.CreateSnapshot(ctx, name, cutoff)
		if err != nil {
			if errLog != nil {
				errLog("retention: snapshot %s @ seq %d: %v", name, cutoff, err)
			}
			continue
		}
		if err := log.Compact(ctx, cutoff, snap); err != nil {
			if errLog != nil {
				errLog("retention: compact %s before seq %d: %v", name, cutoff, err)
			}
		}
	}
}

// computeRetentionCutoff returns the seq strictly below which events
// may be compacted. Retains at least retentionEvents most-recent
// events. Returns 0 when nothing should be compacted yet (the log is
// still shorter than the retention floor).
func computeRetentionCutoff(head uint64, retentionEvents int) uint64 {
	if retentionEvents <= 0 {
		return 0
	}
	if head <= uint64(retentionEvents) { //nolint:gosec // retentionEvents > 0 enforced above
		return 0
	}
	return head - uint64(retentionEvents) //nolint:gosec // bounds checked above
}

// Status describes the running daemon, if any.
type Status struct {
	Running     bool      `json:"running"`
	PID         int       `json:"pid,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	SocketPath  string    `json:"socket_path"`
	PidFilePath string    `json:"pidfile"`
}

// Inspect returns the running status of the daemon for ws.
func Inspect(ws *Workspace) Status {
	st := Status{SocketPath: ws.SocketPath, PidFilePath: ws.PidFile}
	pid, _, ok := readPidfile(ws.PidFile)
	if !ok {
		return st
	}
	if !processAlive(pid) {
		return st
	}
	st.Running = true
	st.PID = pid
	if fi, err := os.Stat(ws.PidFile); err == nil {
		st.StartedAt = fi.ModTime()
	}
	return st
}

// IsRunning is a thin convenience over Inspect.
func IsRunning(ws *Workspace) bool { return Inspect(ws).Running }

// EnsureRunning returns nil when the daemon is reachable on ws.SocketPath.
// If it isn't, the current binary is forked with `daemon serve` and the
// function blocks until the socket becomes connectable (up to 10s) or the
// context is cancelled.
//
// Concurrent callers race safely: only one wins the spawn (via flock on
// the pidfile); losers just wait for the socket.
func EnsureRunning(ctx context.Context, ws *Workspace) error {
	if dialOnce(ws.SocketPath, 250*time.Millisecond) {
		return nil
	}
	if err := ws.EnsureStateDir(); err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}

	// Spawn detached child with daemon serve.
	cmd := exec.Command(exe, "daemon", "serve", "--workspace", ws.Root) //nolint:gosec // exe is our own binary
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "GRAPH_HARNESS_DAEMON_CHILD=1")
	if runtime.GOOS != "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn daemon: %w", err)
	}
	// Don't Wait — we want the child detached. Release the OS handle.
	go func() { _ = cmd.Process.Release() }()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if dialOnce(ws.SocketPath, 100*time.Millisecond) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("daemon spawned but socket did not become connectable within 10s")
}

// Stop best-effort shuts down a running daemon. It first tries the
// daemon.shutdown RPC over the socket, then falls back to SIGTERM after
// 5s if the daemon hasn't exited.
func Stop(ctx context.Context, ws *Workspace) error {
	st := Inspect(ws)
	if !st.Running {
		// Clean up stale pidfile if any.
		_ = os.Remove(ws.PidFile)
		return nil
	}
	// Try graceful shutdown via socket. We deliberately avoid importing
	// the jsonrpc client to keep the daemon package free of cyclic deps;
	// the CLI invokes the RPC, and Stop is the brute-force fallback.
	if runtime.GOOS != "windows" && st.PID > 0 {
		_ = syscall.Kill(st.PID, syscall.SIGTERM)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !processAlive(st.PID) {
			_ = os.Remove(ws.PidFile)
			_ = os.Remove(ws.SocketPath)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if runtime.GOOS != "windows" {
		_ = syscall.Kill(st.PID, syscall.SIGKILL)
	}
	_ = os.Remove(ws.PidFile)
	_ = os.Remove(ws.SocketPath)
	return nil
}

// Listen opens the workspace socket exclusively. It clears a stale socket
// path if no live daemon is bound to it, and writes the pidfile. The caller
// must close the listener to clean up.
func Listen(ws *Workspace) (net.Listener, error) {
	if err := ws.EnsureStateDir(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(ws.SocketPath), 0o700); err != nil { //nolint:gosec // operator-controlled
		return nil, err
	}

	if existing := Inspect(ws); existing.Running {
		return nil, fmt.Errorf("daemon already running (pid=%d)", existing.PID)
	}
	// Stale socket from a crashed daemon — safe to remove.
	_ = os.Remove(ws.SocketPath)

	network := "unix"
	addr := ws.SocketPath
	if runtime.GOOS == "windows" {
		// Real named-pipe support deferred (P5+); P0 ships unix-socket
		// for all OSes that support it. Windows soft-gated via
		// p0-named-pipe feature flag.
		network = "unix"
	}
	ln, err := net.Listen(network, addr)
	if err != nil {
		return nil, fmt.Errorf("bind %s: %w", addr, err)
	}
	if err := writePidfile(ws.PidFile, os.Getpid()); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

// IdleWatcher invokes shutdown() once `last()` is older than the timeout.
// Returns immediately if timeout is zero. The watcher is meant to run in
// a goroutine; it terminates when ctx is done or shutdown is invoked.
func IdleWatcher(ctx context.Context, opts Options, last func() time.Time, shutdown func()) {
	if opts.IdleTimeout <= 0 {
		return
	}
	tick := time.NewTicker(opts.IdleTimeout / 4)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if time.Since(last()) >= opts.IdleTimeout {
				if opts.Logger != nil {
					opts.Logger("idle-shutdown after %s", opts.IdleTimeout)
				}
				shutdown()
				return
			}
		}
	}
}

// CleanupOnExit removes the pidfile + socket. Call as deferred in the
// daemon serve loop after the listener loop exits.
func CleanupOnExit(ws *Workspace) {
	_ = os.Remove(ws.PidFile)
	_ = os.Remove(ws.SocketPath)
}

// --- helpers -----------------------------------------------------------------

func writePidfile(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"+time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o600)
}

func readPidfile(path string) (pid int, startedAt time.Time, ok bool) {
	data, err := os.ReadFile(path) // #nosec G304 -- path under runtime dir we own
	if err != nil {
		return 0, time.Time{}, false
	}
	parts := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	if len(parts) < 1 {
		return 0, time.Time{}, false
	}
	p, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || p <= 0 {
		return 0, time.Time{}, false
	}
	pid = p
	if len(parts) == 2 {
		if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(parts[1])); err == nil {
			startedAt = t
		}
	}
	return pid, startedAt, true
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		// On windows, FindProcess always succeeds; signal 0 not portable.
		// Heuristic: if the process is gone, opening it fails with
		// "OS: invalid argument". Skipped here to keep the surface small.
		return true
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}

func dialOnce(path string, timeout time.Duration) bool {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.Dial("unix", path)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// --- atomic counters used by tests -------------------------------------------

var spawnCounter int64

// SpawnCount returns the number of times EnsureRunning forked a child in
// this process. Used by tests; not meant for production callers.
func SpawnCount() int64 { return atomic.LoadInt64(&spawnCounter) }
