package detect

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Execer abstracts subprocess invocation so detectors can be unit-
// tested without spawning real binaries. exec.Command is the
// production implementation (DefaultExecer); tests inject a fake.
type Execer interface {
	// Run invokes name with args and returns combined stdout + stderr,
	// trimmed of surrounding whitespace, plus any error from the
	// process. ctx bounds the call.
	Run(ctx context.Context, name string, args ...string) (string, error)
	// LookPath mirrors exec.LookPath; tests fake $PATH presence.
	LookPath(name string) (string, bool)
	// Stat mirrors os.Stat. Tests fake filesystem presence.
	Stat(path string) (os.FileInfo, error)
	// Getenv mirrors os.Getenv. Tests fake env state.
	Getenv(key string) string
}

// DefaultExecer is the production Execer.
var DefaultExecer Execer = realExecer{}

// PerProbeTimeout caps every individual subprocess invocation made by
// realExecer.Run. Probes like `bun pm bin`, `pnpm bin`, `npm root -g`
// can hang indefinitely on misbehaving installs (network probes,
// stalled lockfiles, daemon takeovers); this timeout ensures a single
// stuck binary cannot freeze the entire CLI command. Override with
// GRAPH_HARNESS_PROBE_TIMEOUT_MS for debugging.
const defaultProbeTimeout = 1500 * time.Millisecond

type realExecer struct{}

func (realExecer) Run(ctx context.Context, name string, args ...string) (string, error) {
	timeout := defaultProbeTimeout
	if v := os.Getenv("GRAPH_HARNESS_PROBE_TIMEOUT_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, name, args...) //nolint:gosec // detector probes use known binary names
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (realExecer) LookPath(name string) (string, bool) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", false
	}
	return p, true
}

func (realExecer) Stat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

func (realExecer) Getenv(key string) string {
	return os.Getenv(key)
}

// probeFilePath returns a ProbeStep recording whether path exists as a
// regular (or symlinked) executable file. found = true when stat
// succeeds and the entry is not a directory.
func probeFilePath(ex Execer, location string) ProbeStep {
	info, err := ex.Stat(location)
	if err != nil {
		reason := "not present"
		if !os.IsNotExist(err) {
			reason = err.Error()
		}
		return ProbeStep{Location: location, Found: false, Reason: reason}
	}
	if info.IsDir() {
		return ProbeStep{Location: location, Found: false, Reason: "is a directory"}
	}
	return ProbeStep{Location: location, Found: true}
}

// probePATH returns a ProbeStep for `exec.LookPath(name)`.
func probePATH(ex Execer, name string) ProbeStep {
	p, ok := ex.LookPath(name)
	if !ok {
		return ProbeStep{Location: fmt.Sprintf("$PATH (%s)", name), Found: false, Reason: "not on PATH"}
	}
	return ProbeStep{Location: p, Found: true}
}

// probeRunForBin runs a discovery command (e.g. `bun pm bin`) and
// returns the bin directory it printed plus an informational ProbeStep
// recording what was discovered. The step is Found=false on purpose:
// discovery alone doesn't prove the binary exists, only the
// subsequent file-presence step does. firstFound therefore walks past
// the discovery step and only matches when the actual binary file is
// confirmed by probeFilePath.
func probeRunForBin(ctx context.Context, ex Execer, label, name string, args ...string) (ProbeStep, string) {
	out, err := ex.Run(ctx, name, args...)
	if err != nil {
		return ProbeStep{Location: label, Found: false, Reason: err.Error()}, ""
	}
	dir := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	if dir == "" {
		return ProbeStep{Location: label, Found: false, Reason: "empty output"}, ""
	}
	return ProbeStep{Location: label + " → " + dir, Found: false, Reason: "discovered bin dir; probing for binary next"}, dir
}

// firstFound walks steps in order; on the first found = true entry it
// returns (path, source, true). source is derived by the caller-provided
// classify function so each detector can decide whether a hit was
// project-local, ecosystem-user, or PATH.
func firstFound(steps []ProbeStep, classify func(int) ToolSource) (string, ToolSource, bool) {
	for i, s := range steps {
		if s.Found {
			return s.Location, classify(i), true
		}
	}
	return "", "", false
}

// fileExists is a helper used by detectors looking for marker files
// (.venv, deno.json, package.json, etc.).
func fileExists(ex Execer, path string) bool {
	info, err := ex.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// dirExists is a helper used to detect workspace-local directories
// (node_modules/.bin, .venv/bin).
func dirExists(ex Execer, path string) bool {
	info, err := ex.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// versionFromCommand runs name with the given args, captures stdout,
// and returns a single-line trimmed version string. Empty on failure.
// We do not propagate the error — version is informational.
func versionFromCommand(ctx context.Context, ex Execer, name string, args ...string) string {
	out, err := ex.Run(ctx, name, args...)
	if err != nil {
		return ""
	}
	// Most version outputs are one line; some (gopls) print multi-line
	// — collapse to the first non-empty.
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

