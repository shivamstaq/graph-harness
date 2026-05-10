package detect

import (
	"context"
	"path/filepath"
	"testing"
)

// Workspace .venv/bin wins over pipx and PATH.
func TestPyDetector_VenvWinsOverPipxAndPATH(t *testing.T) {
	root := "/work/foo"
	fx := newFakeExecer()
	fx.touchDir(filepath.Join(root, ".venv"))
	fx.touchDir(filepath.Join(root, ".venv", "bin"))
	fx.touchFile(filepath.Join(root, ".venv", "bin", "pyright-langserver"))
	// pipx also has it.
	fx.putBinary("pipx", "/usr/bin/pipx")
	fx.putRun("pipx list --json", `{"venvs": {"pyright": {"metadata": {"main_package": {"app_paths": [{"__Path__": "/home/u/.local/pipx/venvs/pyright/bin/pyright-langserver"}]}}}}}`, nil)
	fx.touchFile("/home/u/.local/pipx/venvs/pyright/bin/pyright-langserver")
	// Version probe for the .venv copy.
	fx.putRun(filepath.Join(root, ".venv", "bin", "pyright-langserver")+" --version", "pyright 1.1.350", nil)

	d := NewPythonDetectorWithExecer(fx)
	got, _ := d.Probe(context.Background(), root)
	pyr := findTool(got, "pyright-langserver")
	if pyr.Status != StatusAvailable {
		t.Fatalf("status: %q", pyr.Status)
	}
	if pyr.Source != SourceProjectLocal {
		t.Fatalf("source: want project_local, got %q", pyr.Source)
	}
	want := filepath.Join(root, ".venv", "bin", "pyright-langserver")
	if pyr.Path != want {
		t.Fatalf("path: want %q, got %q", want, pyr.Path)
	}
}

// VIRTUAL_ENV env var picked up when no workspace .venv.
func TestPyDetector_VirtualEnvEnvVar(t *testing.T) {
	root := "/work/foo"
	fx := newFakeExecer()
	fx.env["VIRTUAL_ENV"] = "/home/u/.virtualenvs/proj"
	fx.touchFile("/home/u/.virtualenvs/proj/bin/pyright-langserver")
	fx.putRun("/home/u/.virtualenvs/proj/bin/pyright-langserver --version", "pyright 1.1.350", nil)

	d := NewPythonDetectorWithExecer(fx)
	got, _ := d.Probe(context.Background(), root)
	pyr := findTool(got, "pyright-langserver")
	if pyr.Status != StatusAvailable {
		t.Fatalf("status: %q", pyr.Status)
	}
	if pyr.Source != SourceProjectLocal {
		t.Fatalf("source: want project_local (VIRTUAL_ENV), got %q", pyr.Source)
	}
}

// pipx-managed venv beats PATH.
func TestPyDetector_PipxBeatsPATH(t *testing.T) {
	root := "/work/foo"
	fx := newFakeExecer()
	fx.putBinary("pipx", "/usr/bin/pipx")
	fx.putRun("pipx list --json", `{"venvs": {"pyright": {"metadata": {"main_package": {"app_paths": [{"__Path__": "/home/u/.local/pipx/venvs/pyright/bin/pyright-langserver"}]}}}}}`, nil)
	fx.touchFile("/home/u/.local/pipx/venvs/pyright/bin/pyright-langserver")
	fx.putBinary("pyright-langserver", "/usr/bin/pyright-langserver")
	fx.putRun("/home/u/.local/pipx/venvs/pyright/bin/pyright-langserver --version", "pyright 1.1.350", nil)

	d := NewPythonDetectorWithExecer(fx)
	got, _ := d.Probe(context.Background(), root)
	pyr := findTool(got, "pyright-langserver")
	if pyr.Source != SourceEcosystemUser {
		t.Fatalf("source: %q", pyr.Source)
	}
	if pyr.Path != "/home/u/.local/pipx/venvs/pyright/bin/pyright-langserver" {
		t.Fatalf("path: %q", pyr.Path)
	}
}

// Missing pyright + .venv detected → preferred install hint is pipx.
func TestPyDetector_InstallHintPipxWhenVenvDetected(t *testing.T) {
	root := "/work/foo"
	fx := newFakeExecer()
	fx.touchDir(filepath.Join(root, ".venv"))

	d := NewPythonDetectorWithExecer(fx)
	got, _ := d.Probe(context.Background(), root)

	pyr := findTool(got, "pyright-langserver")
	if pyr.Status != StatusMissing {
		t.Fatalf("status: %q", pyr.Status)
	}
	if pyr.InstallHints[0].Manager != "pipx" {
		t.Fatalf("manager: want pipx, got %q", pyr.InstallHints[0].Manager)
	}
	if pyr.InstallHints[0].Command != "pipx install pyright" {
		t.Fatalf("command: %q", pyr.InstallHints[0].Command)
	}
}

// Missing pyright + pipx on PATH (no .venv) → preferred install hint is pipx.
func TestPyDetector_InstallHintPipxBeatsPipUv(t *testing.T) {
	fx := newFakeExecer()
	fx.putBinary("pipx", "/usr/bin/pipx")
	fx.putBinary("pip", "/usr/bin/pip")
	fx.putBinary("uv", "/usr/bin/uv")
	fx.putRun("pipx list --json", `{"venvs": {}}`, nil)

	d := NewPythonDetectorWithExecer(fx)
	got, _ := d.Probe(context.Background(), "/work/foo")
	pyr := findTool(got, "pyright-langserver")
	if pyr.InstallHints[0].Manager != "pipx" {
		t.Fatalf("manager: want pipx, got %q", pyr.InstallHints[0].Manager)
	}
}

// Missing pyright + pip on PATH (no pipx, no .venv) → pip preferred over uv.
func TestPyDetector_InstallHintPipBeatsUv(t *testing.T) {
	fx := newFakeExecer()
	fx.putBinary("pip", "/usr/bin/pip")
	fx.putBinary("uv", "/usr/bin/uv")

	d := NewPythonDetectorWithExecer(fx)
	got, _ := d.Probe(context.Background(), "/work/foo")
	pyr := findTool(got, "pyright-langserver")
	if pyr.InstallHints[0].Manager != "pip" {
		t.Fatalf("manager: want pip, got %q", pyr.InstallHints[0].Manager)
	}
	if pyr.InstallHints[0].Command != "pip install --user pyright" {
		t.Fatalf("command: %q", pyr.InstallHints[0].Command)
	}
}

// Missing pyright + only uv on PATH → uv preferred (after pipx and pip).
func TestPyDetector_InstallHintUvLast(t *testing.T) {
	fx := newFakeExecer()
	fx.putBinary("uv", "/usr/bin/uv")

	d := NewPythonDetectorWithExecer(fx)
	got, _ := d.Probe(context.Background(), "/work/foo")
	pyr := findTool(got, "pyright-langserver")
	if pyr.InstallHints[0].Manager != "uv" {
		t.Fatalf("manager: want uv, got %q", pyr.InstallHints[0].Manager)
	}
	if pyr.InstallHints[0].Command != "uv tool install pyright" {
		t.Fatalf("command: %q", pyr.InstallHints[0].Command)
	}
}

// scip-python is npm-distributed regardless of Python project shape.
func TestPyDetector_ScipPythonInstallIsNpm(t *testing.T) {
	root := "/work/foo"
	fx := newFakeExecer()
	fx.touchDir(filepath.Join(root, ".venv"))

	d := NewPythonDetectorWithExecer(fx)
	got, _ := d.Probe(context.Background(), root)

	scip := findTool(got, "scip-python")
	if scip.Status != StatusMissing {
		t.Fatalf("status: %q", scip.Status)
	}
	if scip.InstallHints[0].Manager != "npm" {
		t.Fatalf("manager: %q", scip.InstallHints[0].Manager)
	}
}

// ResolveVenv finds workspace .venv when present.
func TestResolveVenv_WorkspaceVenv(t *testing.T) {
	root := "/work/foo"
	fx := newFakeExecer()
	fx.touchDir(filepath.Join(root, ".venv"))
	fx.touchDir(filepath.Join(root, ".venv", "bin"))
	got := ResolveVenv(fx, root)
	if got != filepath.Join(root, ".venv") {
		t.Fatalf("got %q", got)
	}
}

// ResolveVenv falls back to VIRTUAL_ENV.
func TestResolveVenv_VirtualEnvFallback(t *testing.T) {
	fx := newFakeExecer()
	fx.env["VIRTUAL_ENV"] = "/home/u/.virtualenvs/proj"
	fx.touchDir("/home/u/.virtualenvs/proj")
	got := ResolveVenv(fx, "/no-such-workspace")
	if got != "/home/u/.virtualenvs/proj" {
		t.Fatalf("got %q", got)
	}
}

// ResolveVenv returns empty when nothing matches.
func TestResolveVenv_None(t *testing.T) {
	fx := newFakeExecer()
	got := ResolveVenv(fx, "/no-such-workspace")
	if got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}
