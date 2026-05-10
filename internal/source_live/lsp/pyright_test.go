package lsp

import (
	"reflect"
	"testing"
)

// mergePyrightInitOpts adds pythonPath while preserving the
// python.analysis.* block.
func TestMergePyrightInitOpts_AddsPythonPath(t *testing.T) {
	in := map[string]any{
		"python": map[string]any{
			"analysis": map[string]any{
				"openFilesOnly": true,
			},
		},
	}
	got := mergePyrightInitOpts(in, "/work/.venv/bin/python")
	py, ok := got["python"].(map[string]any)
	if !ok {
		t.Fatalf("python block missing: %#v", got)
	}
	if py["pythonPath"] != "/work/.venv/bin/python" {
		t.Fatalf("pythonPath: %v", py["pythonPath"])
	}
	analysis, ok := py["analysis"].(map[string]any)
	if !ok {
		t.Fatalf("analysis block missing: %#v", py)
	}
	if analysis["openFilesOnly"] != true {
		t.Fatalf("analysis preserved? %#v", analysis)
	}
}

// nil opts should yield a fresh map with python.pythonPath only.
func TestMergePyrightInitOpts_NilOpts(t *testing.T) {
	got := mergePyrightInitOpts(nil, "/work/.venv/bin/python")
	want := map[string]any{
		"python": map[string]any{
			"pythonPath": "/work/.venv/bin/python",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

// Original input must not be mutated.
func TestMergePyrightInitOpts_DoesNotMutateInput(t *testing.T) {
	in := map[string]any{
		"python": map[string]any{
			"analysis": map[string]any{"openFilesOnly": true},
		},
	}
	_ = mergePyrightInitOpts(in, "/work/.venv/bin/python")
	py, _ := in["python"].(map[string]any)
	if _, exists := py["pythonPath"]; exists {
		t.Fatalf("input mutated: %#v", py)
	}
}
