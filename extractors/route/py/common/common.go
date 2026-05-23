// Package common holds shared helpers for the Python route extractors
// (Django, FastAPI, Flask). The three extractors live in sibling Go
// packages so each registers its own name at init() time; the
// AST-walking + content-id + selector-building helpers below are
// language-level concerns that don't change per framework.
package common

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"

	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// FileChangedPayload mirrors internal/daemon.FileChangedPayload. We do
// not import the daemon package (extractors live below the daemon in
// the dependency graph), so we re-declare the shape locally; this is
// safe because the on-the-wire JSON is the contract.
type FileChangedPayload struct {
	Path     string `json:"path"`
	Language string `json:"language,omitempty"`
}

// DecodeFileChanged unpacks the JSON payload of a code.core.FileChanged
// event into the locally-declared shape. Returns the empty struct +
// error on malformed payloads so callers can short-circuit cleanly.
func DecodeFileChanged(ev kernel.Event) (FileChangedPayload, error) {
	var p FileChangedPayload
	if len(ev.Payload) == 0 {
		return p, nil
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return p, fmt.Errorf("decode FileChanged payload: %w", err)
	}
	return p, nil
}

// IsPythonPath reports whether the path looks like a Python source
// file. Used to short-circuit OnEvent for files that other
// extractor families own.
func IsPythonPath(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".py")
}

// ReadSource reads the file at workspace-relative `relPath` from
// `workspace`. Returns nil bytes + nil error if the file does not
// exist (e.g. a delete masquerading as FileChanged); callers treat
// missing files as "nothing to extract."
func ReadSource(workspace, relPath string) ([]byte, error) {
	if workspace == "" {
		// Tests may pass an empty workspace + an already-relative
		// testdata path. Read straight from CWD.
		return os.ReadFile(relPath)
	}
	abs := relPath
	if !filepath.IsAbs(relPath) {
		abs = filepath.Join(workspace, relPath)
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", abs, err)
	}
	return b, nil
}

// ParsedFile bundles the tree-sitter parser + tree handle so callers
// can walk the AST without leaking resource management. Caller MUST
// Close() the returned ParsedFile when done.
type ParsedFile struct {
	parser *tree_sitter.Parser
	tree   *tree_sitter.Tree
	Src    []byte
}

// Root returns the root node of the parsed tree.
func (p *ParsedFile) Root() *tree_sitter.Node { return p.tree.RootNode() }

// Close releases tree-sitter resources.
func (p *ParsedFile) Close() {
	if p.tree != nil {
		p.tree.Close()
	}
	if p.parser != nil {
		p.parser.Close()
	}
}

// ParsePython parses the given source bytes with the tree-sitter
// Python grammar. Returns a ParsedFile that the caller must Close.
func ParsePython(src []byte) (*ParsedFile, error) {
	parser := tree_sitter.NewParser()
	if err := parser.SetLanguage(tree_sitter.NewLanguage(tree_sitter_python.Language())); err != nil {
		parser.Close()
		return nil, fmt.Errorf("set python language: %w", err)
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		parser.Close()
		return nil, fmt.Errorf("tree-sitter parse returned nil tree")
	}
	return &ParsedFile{parser: parser, tree: tree, Src: src}, nil
}

// NodeText returns the source-text slice for `n`.
func NodeText(n *tree_sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	return n.Utf8Text(src)
}

// StringLiteralValue extracts the literal string out of a Python
// `string` AST node — strips the surrounding quotes plus any string
// prefix (r, b, f, etc.). Returns ("", false) if `n` is not a string
// literal we recognize.
//
// We intentionally do NOT attempt to evaluate f-strings — a computed
// path collapses to "" which the caller maps to ConfidenceComputed.
func StringLiteralValue(n *tree_sitter.Node, src []byte) (string, bool) {
	if n == nil || n.Kind() != "string" {
		return "", false
	}
	// Look for the inner string_content node — present in modern
	// tree-sitter-python builds. Falls back to a manual quote-strip
	// for older grammar versions.
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Kind() == "string_content" {
			return c.Utf8Text(src), true
		}
		// f-string: bail, the caller marks confidence-computed.
		if c.Kind() == "interpolation" {
			return "", false
		}
	}
	raw := n.Utf8Text(src)
	// Strip leading prefix letters (r, b, f, u, R, B, F, U combinations).
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if ch == '"' || ch == '\'' {
			raw = raw[i:]
			break
		}
	}
	if len(raw) >= 2 {
		first := raw[0]
		if (first == '"' || first == '\'') && raw[len(raw)-1] == first {
			return raw[1 : len(raw)-1], true
		}
	}
	return "", false
}

// QualifiedNameSelector builds a SelectorRef anchored at the
// qualified_name of the handler function. The extractor lets the
// Dispatcher's EntityRefCache resolve this to a code.core Function
// row at extract time (P2.T04).
func QualifiedNameSelector(qn string) code_framework.SelectorRef {
	return code_framework.SelectorRef{
		Unique:  true,
		Anchors: []code_framework.Anchor{{Kind: "qualified_name", Value: qn}},
	}
}

// PathGlobSelector builds a SelectorRef anchored at the file path —
// used as a fallback when the handler reference cannot be resolved to
// a qualified name (e.g. Django's `views.foo` reference where the
// view function lives in a sibling module the extractor cannot read).
func PathGlobSelector(path string) code_framework.SelectorRef {
	return code_framework.SelectorRef{
		Anchors: []code_framework.Anchor{{Kind: "path_glob", Value: path}},
	}
}

// Provenance builds a code_framework.Provenance row for the given
// confidence + extractor name. Freshness defaults to Fresh because
// the file was parsed from disk this OnEvent call; ProducedSeq is
// stamped by the dispatcher post-emit.
func Provenance(extractorName string, confidence float64) code_framework.Provenance {
	return code_framework.Provenance{
		Confidence:  confidence,
		Freshness:   kernel.FreshnessCurrent,
		SourceClass: []kernel.SourceClass{code_framework.SourceExtractorFramework},
		ProducedBy:  "extractor:framework:" + extractorName,
	}
}

// ModuleName derives a dotted Python module path from a workspace-
// relative file path. Mirrors internal/source_live.parser_py.go's
// pyModuleName so the qualified_name the extractor emits matches the
// code.core Function row's QN.
func ModuleName(path string) string {
	rel := filepath.ToSlash(path)
	rel = strings.TrimSuffix(rel, ".py")
	rel = strings.TrimSuffix(rel, "/__init__")
	rel = strings.TrimPrefix(rel, "./")
	return strings.ReplaceAll(rel, "/", ".")
}

// QualifiedName composes `module` and `name` into a dotted qualified
// name. Either may be empty.
func QualifiedName(module, name string) string {
	if module == "" {
		return name
	}
	if name == "" {
		return module
	}
	return module + "." + name
}

// MethodCanon canonicalizes a method string to upper-case GET/POST/...,
// stripping surrounding quotes. Returns "" for the empty input.
func MethodCanon(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'")
	return strings.ToUpper(s)
}

// JoinPath joins a router prefix and a route subpath, normalizing
// duplicate slashes. Both inputs may be empty; an empty result is
// returned as "/" so route anchors always have a leading slash.
func JoinPath(prefix, sub string) string {
	prefix = strings.TrimRight(prefix, "/")
	sub = strings.TrimLeft(sub, "/")
	switch {
	case prefix == "" && sub == "":
		return "/"
	case prefix == "":
		return "/" + sub
	case sub == "":
		if prefix == "" {
			return "/"
		}
		return prefix
	}
	return prefix + "/" + sub
}

// ConfidenceLiteral is the score assigned when both the decorator
// and the path are literal (rubric: 0.95).
const ConfidenceLiteral = 0.95

// ConfidenceComputed is the score assigned when the path is a
// computed expression (e.g. f-string, variable) and the extractor
// can only fall back to a partial match (rubric: 0.85).
const ConfidenceComputed = 0.85

// ConfidenceDynamic is the score assigned when the route is reached
// through dynamic include / wildcard reference (rubric: 0.7).
const ConfidenceDynamic = 0.70
