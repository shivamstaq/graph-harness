package detect

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// fakeExecer is the test-only Execer used by every detector unit test.
// It replays canned responses for Run/LookPath/Stat/Getenv based on a
// deterministic configuration the test sets up.
type fakeExecer struct {
	// pathExecs lists the binaries the fake claims are on $PATH. Values
	// are the resolved absolute paths LookPath returns.
	pathExecs map[string]string
	// runResults maps "<binary> <args joined by space>" → (stdout, err).
	runResults map[string]fakeRunResult
	// files is the fake filesystem; values track whether the entry is a
	// directory.
	files map[string]bool // path → isDir
	// env values.
	env map[string]string
}

type fakeRunResult struct {
	out string
	err error
}

func newFakeExecer() *fakeExecer {
	return &fakeExecer{
		pathExecs:  map[string]string{},
		runResults: map[string]fakeRunResult{},
		files:      map[string]bool{},
		env:        map[string]string{},
	}
}

// Run satisfies Execer.
func (f *fakeExecer) Run(_ context.Context, name string, args ...string) (string, error) {
	key := name + " " + strings.Join(args, " ")
	key = strings.TrimSpace(key)
	if r, ok := f.runResults[key]; ok {
		return r.out, r.err
	}
	return "", errors.New("fakeExecer: no canned result for " + key)
}

// LookPath satisfies Execer.
func (f *fakeExecer) LookPath(name string) (string, bool) {
	p, ok := f.pathExecs[name]
	return p, ok
}

// Stat satisfies Execer.
func (f *fakeExecer) Stat(path string) (os.FileInfo, error) {
	isDir, ok := f.files[path]
	if !ok {
		return nil, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
	}
	return &fakeFileInfo{name: filepath.Base(path), isDir: isDir}, nil
}

// Getenv satisfies Execer.
func (f *fakeExecer) Getenv(key string) string {
	return f.env[key]
}

// touchFile records the path as a regular file in the fake fs and
// returns the path so test setup can chain calls.
func (f *fakeExecer) touchFile(path string) string {
	f.files[path] = false
	return path
}

// touchDir records path as a directory in the fake fs.
func (f *fakeExecer) touchDir(path string) string {
	f.files[path] = true
	return path
}

// putRun installs a canned Run response.
func (f *fakeExecer) putRun(cmd string, out string, err error) {
	f.runResults[strings.TrimSpace(cmd)] = fakeRunResult{out: out, err: err}
}

// putBinary marks name as discoverable on $PATH at resolved.
func (f *fakeExecer) putBinary(name, resolved string) {
	f.pathExecs[name] = resolved
}

// fakeFileInfo is a minimal FileInfo for the fake fs.
type fakeFileInfo struct {
	name  string
	isDir bool
}

func (f *fakeFileInfo) Name() string       { return f.name }
func (f *fakeFileInfo) Size() int64        { return 0 }
func (f *fakeFileInfo) Mode() os.FileMode  { return 0o755 }
func (f *fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f *fakeFileInfo) IsDir() bool        { return f.isDir }
func (f *fakeFileInfo) Sys() any           { return nil }
