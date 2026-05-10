package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/source_live/detect"
)

func sampleReports() []detect.Report {
	return []detect.Report{
		{
			LanguageID: "go",
			Tools: []detect.ToolReport{
				{
					Name: "gopls", Class: detect.ToolClassLSP, ServerID: "gopls",
					Status: detect.StatusAvailable, Path: "/home/u/go/bin/gopls",
					Source: detect.SourcePATH, Version: "v0.16.1",
					ProbeChain: []detect.ProbeStep{
						{Location: "PATH lookup `gopls`", Found: true},
					},
				},
				{
					Name: "scip-go", Class: detect.ToolClassSCIP,
					Status: detect.StatusMissing,
					ProbeChain: []detect.ProbeStep{
						{Location: "PATH lookup `scip-go`", Found: false, Reason: "not in PATH"},
					},
					InstallHints: []detect.InstallHint{
						{Manager: "go install", Command: "go install github.com/scip-code/scip-go/cmd/scip-go@latest", Preferred: true},
					},
				},
				{
					Name: "tree-sitter-go", Class: detect.ToolClassParser,
					Status: detect.StatusEmbedded, Source: detect.SourceEmbedded,
				},
			},
		},
	}
}

// Default JSON is slim, pretty-printed, and excludes verbose-only fields.
func TestWriteDoctorJSON_DefaultIsSlim(t *testing.T) {
	var buf bytes.Buffer
	if err := writeDoctorJSON(&buf, "/work/foo", sampleReports(), false); err != nil {
		t.Fatalf("writeDoctorJSON: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "\n  \"") {
		t.Fatalf("expected pretty-printed JSON, got compact:\n%s", out)
	}
	for _, banned := range []string{"probe_chain", "\"source\":", "server_id"} {
		if strings.Contains(out, banned) {
			t.Fatalf("slim JSON should not contain %q:\n%s", banned, out)
		}
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if got["workspace_root"] != "/work/foo" {
		t.Fatalf("workspace_root: %v", got["workspace_root"])
	}
	for _, key := range []string{"summary", "coverage", "tools", "languages"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing top-level key %q in slim JSON:\n%s", key, out)
		}
	}
	tools := got["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("tools count: want 3, got %d", len(tools))
	}
	first := tools[0].(map[string]any)
	if first["language"] != "go" || first["name"] != "gopls" {
		t.Fatalf("flat tool shape mismatch: %#v", first)
	}
	summary := got["summary"].(map[string]any)
	if int(summary["total"].(float64)) != 3 || int(summary["missing"].(float64)) != 1 {
		t.Fatalf("summary mismatch: %#v", summary)
	}
}

// Verbose JSON adds source + probe_chain to each tool entry.
func TestWriteDoctorJSON_VerboseIncludesProbeChain(t *testing.T) {
	var buf bytes.Buffer
	if err := writeDoctorJSON(&buf, "/work/foo", sampleReports(), true); err != nil {
		t.Fatalf("writeDoctorJSON verbose: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "probe_chain") {
		t.Fatalf("verbose JSON missing probe_chain:\n%s", out)
	}
	if !strings.Contains(out, "\"source\": \"path\"") {
		t.Fatalf("verbose JSON missing source field:\n%s", out)
	}
	if !strings.Contains(out, "\"server_id\": \"gopls\"") {
		t.Fatalf("verbose JSON missing server_id:\n%s", out)
	}
}

// Card-view text renderer emits each section heading and the legend.
func TestRenderDoctor_Default(t *testing.T) {
	var buf bytes.Buffer
	if err := renderDoctor(&buf, "/work/foo", sampleReports(), false); err != nil {
		t.Fatalf("renderDoctor: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Workspace: /work/foo",
		"Languages: go",
		"Coverage",
		"go\n", // language section heading
		"gopls",
		"scip-go",
		"tree-sitter-go",
		"bundled",
		"Legend",
		"Next steps",
		"graph-harness doctor --print-install",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "(embed)") {
		t.Fatalf("output still uses legacy '(embed)' label:\n%s", out)
	}
}

// Full install commands are printed verbatim (no truncation).
func TestRenderDoctor_NoTruncation(t *testing.T) {
	var buf bytes.Buffer
	if err := renderDoctor(&buf, "/work/foo", sampleReports(), false); err != nil {
		t.Fatalf("renderDoctor: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "…") {
		t.Fatalf("output contains truncation ellipsis:\n%s", out)
	}
	const fullCmd = "go install github.com/scip-code/scip-go/cmd/scip-go@latest"
	if !strings.Contains(out, fullCmd) {
		t.Fatalf("install command not printed verbatim:\n%s", out)
	}
}

// Writing to a non-TTY writer (bytes.Buffer) emits no ANSI escapes.
func TestRenderDoctor_NoANSIWhenNotTTY(t *testing.T) {
	var buf bytes.Buffer
	if err := renderDoctor(&buf, "/work/foo", sampleReports(), false); err != nil {
		t.Fatalf("renderDoctor: %v", err)
	}
	if bytes.Contains(buf.Bytes(), []byte{0x1b, '['}) {
		t.Fatalf("non-TTY output contains ANSI escape bytes:\n%s", buf.String())
	}
}

// Verbose mode prints the probe trace before the card view.
func TestRenderDoctor_VerboseHasProbeTrace(t *testing.T) {
	var buf bytes.Buffer
	if err := renderDoctor(&buf, "/work/foo", sampleReports(), true); err != nil {
		t.Fatalf("renderDoctor verbose: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Probe trace") {
		t.Fatalf("verbose mode missing 'Probe trace' heading:\n%s", out)
	}
	if !strings.Contains(out, "go/gopls") || !strings.Contains(out, "(lsp)") {
		t.Fatalf("verbose mode missing per-tool probe header:\n%s", out)
	}
	if !strings.Contains(out, "PATH lookup `gopls`") {
		t.Fatalf("verbose mode missing probe-step location:\n%s", out)
	}
	probeIdx := strings.Index(out, "Probe trace")
	cardIdx := strings.Index(out, "Coverage")
	if probeIdx < 0 || cardIdx < 0 || probeIdx > cardIdx {
		t.Fatalf("probe trace must precede the card view; probe=%d card=%d", probeIdx, cardIdx)
	}
}

// homeShort collapses $HOME but never mutates non-home paths.
func TestHomeShort(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	if got := homeShort(home + "/go/bin/gopls"); got != "~/go/bin/gopls" {
		t.Fatalf("home prefix not collapsed: %q", got)
	}
	if got := homeShort("/usr/local/bin/gopls"); got != "/usr/local/bin/gopls" {
		t.Fatalf("non-home path mutated: %q", got)
	}
}

// Install renderer emits the Preferred command for missing tools and
// skips available tools.
func TestWriteDoctorInstall_OnlyMissingPreferred(t *testing.T) {
	var buf bytes.Buffer
	if err := writeDoctorInstall(&buf, sampleReports()); err != nil {
		t.Fatalf("writeDoctorInstall: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "go install github.com/scip-code/scip-go/cmd/scip-go@latest") {
		t.Fatalf("install: missing scip-go hint:\n%s", out)
	}
	if strings.Contains(out, "# go/gopls") {
		t.Fatalf("install: should not include available tool gopls:\n%s", out)
	}
}

// Index recipe contains the per-language SCIP commands.
func TestWriteDoctorIndexRecipe_IncludesAllLanguages(t *testing.T) {
	var buf bytes.Buffer
	reports := []detect.Report{{
		LanguageID: "go",
		Tools: []detect.ToolReport{{
			Name: "scip-go", Class: detect.ToolClassSCIP, Status: detect.StatusAvailable,
			Path: "/home/u/go/bin/scip-go",
		}},
	}}
	if err := writeDoctorIndexRecipe(&buf, reports); err != nil {
		t.Fatalf("writeDoctorIndexRecipe: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "/home/u/go/bin/scip-go --module-root=. --output=.scip-index/index.go.scip") {
		t.Fatalf("recipe: missing go scip command:\n%s", out)
	}
}

// missingPrimaryExtractors counts only LSP/SCIP entries with status missing.
func TestMissingPrimaryExtractors_Count(t *testing.T) {
	got := missingPrimaryExtractors("", sampleReports())
	if got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
}
