package source_live

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLanguageOf(t *testing.T) {
	cases := map[string]string{
		"a/b.go":         "go",
		"x.ts":           "typescript",
		"x.tsx":          "typescript",
		"x.js":           "typescript",
		"x.jsx":          "typescript",
		"util/helper.py": "python",
		"README.md":      "",
		"go.mod":         "",
	}
	for path, want := range cases {
		if got := LanguageOf(path); got != want {
			t.Errorf("LanguageOf(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestParseFile_DispatchesByExtension(t *testing.T) {
	pf, err := ParseFile("a.go", []byte("package x\nfunc Hi() {}"))
	if err != nil {
		t.Fatalf("go: %v", err)
	}
	if pf == nil || pf.Language != "go" {
		t.Errorf("go dispatch failed: %+v", pf)
	}

	pf, err = ParseFile("a.ts", []byte("export function hi() {}"))
	if err != nil {
		t.Fatalf("ts: %v", err)
	}
	if pf == nil || pf.Language != "typescript" {
		t.Errorf("ts dispatch failed: %+v", pf)
	}

	pf, err = ParseFile("a.py", []byte("def hi(): pass"))
	if err != nil {
		t.Fatalf("py: %v", err)
	}
	if pf == nil || pf.Language != "python" {
		t.Errorf("py dispatch failed: %+v", pf)
	}

	pf, err = ParseFile("README.md", []byte("# hi"))
	if err != nil {
		t.Fatalf("md: %v", err)
	}
	if pf != nil {
		t.Errorf("expected nil ParsedFile for unsupported extension, got %+v", pf)
	}
}

func TestWatcher_EmitsFileParsedOnWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping watcher integration test in short mode")
	}
	dir := t.TempDir()

	w, err := NewWatcher(dir)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Stop()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	target := filepath.Join(dir, "hello.go")
	if err := os.WriteFile(target, []byte("package x\nfunc Hello() {}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	var sawChanged, sawParsed bool
	for !sawParsed {
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for FileParsed; sawChanged=%v", sawChanged)
		case ev, ok := <-w.Events():
			if !ok {
				t.Fatalf("events channel closed early; sawChanged=%v", sawChanged)
			}
			if ev.Err != nil {
				t.Fatalf("event error: %v", ev.Err)
			}
			switch ev.Kind {
			case FileEventChanged:
				sawChanged = true
			case FileEventParsed:
				sawParsed = true
				if ev.Parsed == nil {
					t.Fatalf("FileParsed event had nil Parsed payload")
				}
				if ev.Parsed.Language != "go" {
					t.Errorf("language = %q, want go", ev.Parsed.Language)
				}
			case FileEventRemoved:
				// ignore
			}
		}
	}
}

func TestWatcher_SkipsUninterestingDirs(t *testing.T) {
	for _, name := range []string{".git", "node_modules", "__pycache__", "target"} {
		if !shouldSkipDir(name) {
			t.Errorf("shouldSkipDir(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"src", "internal", "lib"} {
		if shouldSkipDir(name) {
			t.Errorf("shouldSkipDir(%q) = true, want false", name)
		}
	}
}
