package detect

import (
	"context"
	"path/filepath"
	"testing"
)

// node_modules/.bin wins over pnpm-global and PATH.
func TestTSDetector_ProjectLocalNodeModulesWins(t *testing.T) {
	root := "/work/foo"
	fx := newFakeExecer()
	fx.touchFile(filepath.Join(root, "node_modules", ".bin", "typescript-language-server"))
	// pnpm-global also has it.
	fx.putBinary("pnpm", "/usr/bin/pnpm")
	fx.putRun("pnpm bin -g", "/home/u/.local/share/pnpm", nil)
	fx.touchFile("/home/u/.local/share/pnpm/typescript-language-server")
	// PATH has another.
	fx.putBinary("typescript-language-server", "/usr/bin/typescript-language-server")
	fx.putRun(filepath.Join(root, "node_modules", ".bin", "typescript-language-server")+" --version", "4.3.3", nil)

	d := NewTypeScriptDetectorWithExecer(fx)
	got, _ := d.Probe(context.Background(), root)
	tss := findTool(got, "typescript-language-server")
	if tss.Status != StatusAvailable {
		t.Fatalf("status: %q", tss.Status)
	}
	if tss.Source != SourceProjectLocal {
		t.Fatalf("source: want project_local, got %q", tss.Source)
	}
	want := filepath.Join(root, "node_modules", ".bin", "typescript-language-server")
	if tss.Path != want {
		t.Fatalf("path: want %q, got %q", want, tss.Path)
	}
}

// pnpm-global wins over PATH when project node_modules is absent.
func TestTSDetector_PnpmGlobalBeatsPATH(t *testing.T) {
	root := "/work/foo"
	fx := newFakeExecer()
	fx.putBinary("pnpm", "/usr/bin/pnpm")
	fx.putRun("pnpm bin -g", "/home/u/.local/share/pnpm", nil)
	fx.touchFile("/home/u/.local/share/pnpm/scip-typescript")
	fx.putBinary("scip-typescript", "/usr/bin/scip-typescript")
	fx.putRun("/home/u/.local/share/pnpm/scip-typescript --version", "0.3.27", nil)

	d := NewTypeScriptDetectorWithExecer(fx)
	got, _ := d.Probe(context.Background(), root)
	scip := findTool(got, "scip-typescript")
	if scip.Source != SourceEcosystemUser {
		t.Fatalf("source: want ecosystem_user, got %q", scip.Source)
	}
	if scip.Path != "/home/u/.local/share/pnpm/scip-typescript" {
		t.Fatalf("path: %q", scip.Path)
	}
	if scip.Version != "0.3.27" {
		t.Fatalf("version: %q", scip.Version)
	}
}

// Install hints respect lockfile signal: pnpm-lock.yaml → pnpm.
func TestTSDetector_InstallHintPnpmFromLockfile(t *testing.T) {
	root := "/work/foo"
	fx := newFakeExecer()
	fx.touchFile(filepath.Join(root, "pnpm-lock.yaml"))

	d := NewTypeScriptDetectorWithExecer(fx)
	got, _ := d.Probe(context.Background(), root)

	scip := findTool(got, "scip-typescript")
	if scip.Status != StatusMissing {
		t.Fatalf("status: %q", scip.Status)
	}
	if len(scip.InstallHints) == 0 {
		t.Fatal("expected install hints")
	}
	preferred := scip.InstallHints[0]
	if preferred.Manager != "pnpm" {
		t.Fatalf("preferred manager: want pnpm, got %q", preferred.Manager)
	}
	if preferred.Reason != "pnpm-lock.yaml detected" {
		t.Fatalf("reason: %q", preferred.Reason)
	}
}

// Bun lockfile wins over pnpm-lock if both somehow exist.
func TestTSDetector_BunLockfileTakesPriority(t *testing.T) {
	root := "/work/foo"
	fx := newFakeExecer()
	fx.touchFile(filepath.Join(root, "bun.lockb"))
	fx.touchFile(filepath.Join(root, "pnpm-lock.yaml"))

	d := NewTypeScriptDetectorWithExecer(fx)
	got, _ := d.Probe(context.Background(), root)
	tss := findTool(got, "typescript-language-server")
	if len(tss.InstallHints) == 0 {
		t.Fatal("expected install hints")
	}
	if tss.InstallHints[0].Manager != "bun" {
		t.Fatalf("manager: want bun, got %q", tss.InstallHints[0].Manager)
	}
}

// No lockfile + no package.json → npm fallback.
func TestTSDetector_NoLockfileFallsBackToNpm(t *testing.T) {
	fx := newFakeExecer()
	d := NewTypeScriptDetectorWithExecer(fx)
	got, _ := d.Probe(context.Background(), "/work/foo")
	tss := findTool(got, "typescript-language-server")
	if tss.InstallHints[0].Manager != "npm" {
		t.Fatalf("manager: %q", tss.InstallHints[0].Manager)
	}
}
