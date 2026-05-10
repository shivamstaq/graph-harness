// Package detect probes the user's environment to find which LSP
// servers, SCIP indexers, and parser dependencies are usable for
// graph-harness's source.live extractors.
//
// Positioning (SPEC §6.18): graph-harness is a detector and orchestrator
// for external tooling, never a supplier. We never auto-download tools;
// we never maintain a per-user cache. Detection is per-language, with
// project-local-first precedence (workspace node_modules/.bin, .venv,
// vendor, etc. before user/global locations).
//
// Architecture: each supported language registers a Detector
// implementation. The Registry mirrors lsp.Registry. The Orchestrator
// invokes Detector.Probe before LSP/SCIP gather paths and emits
// code.core.ExtractorUnavailable events for any tool with Status =
// StatusMissing. The doctor command renders the same Report payload
// for humans (text) or machines (--json).
package detect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Detector probes one language's tooling ecosystem and returns a
// Report describing every tool graph-harness cares about for that
// language (LSP server, SCIP indexer, parser). Implementations are
// expected to be pure-detection: no subprocess spawning beyond what
// `--version` or path-listing requires; no installs; no mutations.
type Detector interface {
	LanguageID() string
	Probe(ctx context.Context, workspaceRoot string) (Report, error)
}

// Report is one language's detection summary.
type Report struct {
	LanguageID string       `json:"language_id"`
	Tools      []ToolReport `json:"tools"`
}

// ToolReport is one tool's detection result for a language.
type ToolReport struct {
	Name         string        `json:"name"`
	Class        ToolClass     `json:"class"`
	ServerID     string        `json:"server_id,omitempty"`
	Status       ToolStatus    `json:"status"`
	Path         string        `json:"path,omitempty"`
	Source       ToolSource    `json:"source,omitempty"`
	Version      string        `json:"version,omitempty"`
	ProbeChain   []ProbeStep   `json:"probe_chain"`
	InstallHints []InstallHint `json:"install_hints,omitempty"`
}

// ProbeStep records one location the detector checked, in order.
type ProbeStep struct {
	Location string `json:"location"`
	Found    bool   `json:"found"`
	Reason   string `json:"reason,omitempty"`
}

// InstallHint describes how a user could install a missing tool. The
// detector never executes these; they are payload for the doctor
// command's --print-install renderer and the gh://doctor MCP resource.
type InstallHint struct {
	Manager   string `json:"manager"`
	Command   string `json:"command"`
	Preferred bool   `json:"preferred"`
	Reason    string `json:"reason,omitempty"`
}

// ToolClass groups tools by their role in the source.live → code.core
// pipeline. Used to filter doctor output and to drive the
// ExtractorUnavailable event's tool_class field.
type ToolClass string

// Tool-class constants.
const (
	ToolClassLSP    ToolClass = "lsp"
	ToolClassSCIP   ToolClass = "scip"
	ToolClassParser ToolClass = "parser"
)

// ToolStatus is the boolean-with-reason result of a probe.
type ToolStatus string

// Tool-status constants.
const (
	StatusAvailable       ToolStatus = "available"
	StatusMissing         ToolStatus = "missing"
	StatusVersionMismatch ToolStatus = "version_mismatch"
	StatusEmbedded        ToolStatus = "embedded"
)

// ToolSource describes where the resolved binary was found. Surfaces
// in provenance.sources[].produced_by_path so consumers can audit
// which physical install produced a fact.
type ToolSource string

// Tool-source constants.
const (
	SourceProjectLocal  ToolSource = "project_local"
	SourceEcosystemUser ToolSource = "ecosystem_user"
	SourcePATH          ToolSource = "path"
	SourceEmbedded      ToolSource = "embedded"
)

// IdempotencyKey is the dedup key for code.core.ExtractorUnavailable
// events: sha256(workspace_root || language_id || tool_name). The
// orchestrator uses this so repeated detections during a workspace
// sweep emit the event exactly once.
func IdempotencyKey(workspaceRoot, languageID, toolName string) string {
	h := sha256.New()
	h.Write([]byte(workspaceRoot))
	h.Write([]byte{0})
	h.Write([]byte(languageID))
	h.Write([]byte{0})
	h.Write([]byte(toolName))
	return hex.EncodeToString(h.Sum(nil))
}

// Now is the time source used for DetectedAt. Overridden in tests to
// keep golden JSON fixtures stable.
var Now = time.Now
