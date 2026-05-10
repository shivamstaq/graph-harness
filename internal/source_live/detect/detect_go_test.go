package detect

import (
	"context"
	"path/filepath"
	"testing"
)

// Project-local vendor/bin should win over GOBIN, GOPATH, and PATH.
func TestGoDetector_ProjectLocalVendorBinWins(t *testing.T) {
	root := "/work/foo"
	fx := newFakeExecer()
	fx.touchFile(filepath.Join(root, "vendor", "bin", "gopls"))
	// GOBIN also has a copy — should NOT be chosen.
	fx.putBinary("go", "/usr/bin/go")
	fx.putRun("go env GOBIN GOPATH", "/home/u/go/bin\n/home/u/go", nil)
	fx.touchFile("/home/u/go/bin/gopls")
	// Version probe (best-effort).
	fx.putRun(filepath.Join(root, "vendor", "bin", "gopls")+" version", "golang.org/x/tools/gopls v0.16.1", nil)

	d := NewGoDetectorWithExecer(fx)
	got, err := d.Probe(context.Background(), root)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	gopls := findTool(got, "gopls")
	if gopls.Status != StatusAvailable {
		t.Fatalf("status: want available, got %q", gopls.Status)
	}
	if gopls.Source != SourceProjectLocal {
		t.Fatalf("source: want project_local, got %q", gopls.Source)
	}
	wantPath := filepath.Join(root, "vendor", "bin", "gopls")
	if gopls.Path != wantPath {
		t.Fatalf("path: want %q, got %q", wantPath, gopls.Path)
	}
	if gopls.Version != "v0.16.1" {
		t.Fatalf("version: want v0.16.1, got %q", gopls.Version)
	}
}

// GOBIN takes precedence over GOPATH/bin and PATH.
func TestGoDetector_GOBINBeatsPATH(t *testing.T) {
	root := "/work/foo"
	fx := newFakeExecer()
	fx.putBinary("go", "/usr/bin/go")
	fx.putRun("go env GOBIN GOPATH", "/home/u/go/bin\n/home/u/go", nil)
	fx.touchFile("/home/u/go/bin/gopls")
	fx.putBinary("gopls", "/usr/local/bin/gopls") // PATH copy
	fx.putRun("/home/u/go/bin/gopls version", "golang.org/x/tools/gopls v0.17.0", nil)

	d := NewGoDetectorWithExecer(fx)
	got, _ := d.Probe(context.Background(), root)
	gopls := findTool(got, "gopls")
	if gopls.Source != SourceEcosystemUser {
		t.Fatalf("source: want ecosystem_user, got %q", gopls.Source)
	}
	if gopls.Path != "/home/u/go/bin/gopls" {
		t.Fatalf("path: want GOBIN copy, got %q", gopls.Path)
	}
}

// Missing binary records install hint.
func TestGoDetector_MissingEmitsInstallHint(t *testing.T) {
	root := "/work/foo"
	fx := newFakeExecer()
	fx.putBinary("go", "/usr/bin/go")
	fx.putRun("go env GOBIN GOPATH", "\n/home/u/go", nil) // GOBIN empty

	d := NewGoDetectorWithExecer(fx)
	got, _ := d.Probe(context.Background(), root)

	scip := findTool(got, "scip-go")
	if scip.Status != StatusMissing {
		t.Fatalf("status: want missing, got %q", scip.Status)
	}
	if len(scip.InstallHints) == 0 {
		t.Fatal("expected install hints")
	}
	if scip.InstallHints[0].Command != "go install github.com/scip-code/scip-go/cmd/scip-go@latest" {
		t.Fatalf("install command: %q", scip.InstallHints[0].Command)
	}
}

// tree-sitter-go is always present (cgo-bundled).
func TestGoDetector_TreeSitterAlwaysEmbedded(t *testing.T) {
	fx := newFakeExecer()
	d := NewGoDetectorWithExecer(fx)
	got, _ := d.Probe(context.Background(), "/work/foo")
	ts := findTool(got, "tree-sitter-go")
	if ts.Status != StatusEmbedded {
		t.Fatalf("status: %q", ts.Status)
	}
	if ts.Source != SourceEmbedded {
		t.Fatalf("source: %q", ts.Source)
	}
}

func findTool(r Report, name string) ToolReport {
	for _, t := range r.Tools {
		if t.Name == name {
			return t
		}
	}
	return ToolReport{}
}
