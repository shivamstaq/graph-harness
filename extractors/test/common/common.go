// Package common holds helpers shared by the per-framework test
// extractors (go_test, jest, vitest, pytest). The helpers cover three
// recurring concerns:
//
//  1. Decoding the source.live / code.core FileChanged payload and
//     reading the changed file's contents from the workspace root.
//  2. Best-effort subject inference: identify nearby qualified-name
//     references in a test file and fuzzy-match against a test name
//     to produce a code.core SelectorRef the test "probably exercises"
//     (plan §5 risk row — confidence stays medium).
//  3. Best-effort ContractTest discovery: scan a test file's string
//     literals for tokens that match known Event.event_name or
//     Route.path_pattern entries already present in code.framework.
//     The lookup is intentionally tolerant — if the framework store
//     has no entries yet (Pass 1 of Phase 2 spins up extractors in
//     parallel) the helper returns no matches rather than failing.
package common

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// FileChangedPayload mirrors internal/daemon.FileChangedPayload — kept
// local so the extractor package does not import internal/daemon. The
// JSON shape on the kernel bus is `{path, language}`.
type FileChangedPayload struct {
	Path     string `json:"path"`
	Language string `json:"language,omitempty"`
}

// DecodeFileChanged unmarshals the payload from a kernel.Event whose
// Kind is "FileChanged" / "FileRemoved" / "FileParsed". Returns the
// path and language id (best-effort — empty strings on missing fields).
func DecodeFileChanged(ev kernel.Event) (FileChangedPayload, error) {
	var p FileChangedPayload
	if len(ev.Payload) == 0 {
		return p, errors.New("empty payload")
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return p, err
	}
	if p.Path == "" {
		return p, errors.New("payload missing path")
	}
	return p, nil
}

// ReadFile reads the source bytes for path relative to workspace.
// Returns the file contents and the absolute path. A missing file
// yields (nil, "", nil) — the extractor should treat that as "no
// signal" and emit nothing rather than fail.
func ReadFile(workspace, relPath string) ([]byte, string, error) {
	if workspace == "" {
		// In tests the workspace may be empty when the caller already
		// passes an absolute path.
		if filepath.IsAbs(relPath) {
			data, err := os.ReadFile(relPath)
			if errors.Is(err, os.ErrNotExist) {
				return nil, relPath, nil
			}
			return data, relPath, err
		}
		return nil, relPath, nil
	}
	abs := relPath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(workspace, relPath)
	}
	data, err := os.ReadFile(abs)
	if errors.Is(err, os.ErrNotExist) {
		return nil, abs, nil
	}
	return data, abs, err
}

// ---- Test-file naming -------------------------------------------------------

// IsGoTestFile returns true if path ends in "_test.go".
func IsGoTestFile(path string) bool {
	return strings.HasSuffix(path, "_test.go")
}

// IsTSTestFile returns true if path matches Jest/Vitest conventions:
// *.test.ts(x) / *.spec.ts(x) (and the .js equivalents).
func IsTSTestFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	for _, suffix := range []string{
		".test.ts", ".test.tsx", ".test.js", ".test.jsx",
		".spec.ts", ".spec.tsx", ".spec.js", ".spec.jsx",
	} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

// IsPyTestFile returns true if path matches pytest's default
// collection patterns: test_*.py or *_test.py.
func IsPyTestFile(path string) bool {
	base := filepath.Base(path)
	if !strings.HasSuffix(base, ".py") {
		return false
	}
	if strings.HasPrefix(base, "test_") {
		return true
	}
	return strings.HasSuffix(base, "_test.py")
}

// ---- String-literal scanning ------------------------------------------------

// quotedString matches "..." / '...' / `...` (back-tick raw strings),
// allowing simple escape sequences. The capture group holds the
// inner text.
var quotedString = regexp.MustCompile("\"((?:\\\\.|[^\"\\\\])*)\"|'((?:\\\\.|[^'\\\\])*)'|`([^`]*)`")

// ExtractStringLiterals returns the (deduplicated, sorted) set of
// string-literal contents inside src. Coarse — it does not skip
// comments — but ContractTest detection is best-effort and noise
// in the set is harmless because the matcher only emits a ContractTest
// when a literal equals a *known* topic/route from code.framework.
func ExtractStringLiterals(src []byte) []string {
	seen := map[string]struct{}{}
	for _, m := range quotedString.FindAllSubmatch(src, -1) {
		var s string
		switch {
		case m[1] != nil:
			s = string(m[1])
		case m[2] != nil:
			s = string(m[2])
		case m[3] != nil:
			s = string(m[3])
		}
		if s == "" {
			continue
		}
		seen[s] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// ---- Subject inference ------------------------------------------------------

// qualifiedName matches dotted or PascalCase identifiers — a coarse
// approximation of "qualified name" that catches Go's `pkg.Func`,
// TS's `Module.Func` / `Class.method`, and Python's `module.func`.
// At least one dot is required so plain identifiers (which are noisy)
// don't drown out real qualified names; the single-identifier fallback
// is handled by NearbyIdentifiers.
var qualifiedName = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+\b`)

// NearbyQualifiedNames returns every qualified name (dotted form)
// observed in src, deduplicated and sorted. Includes import paths,
// call-site receivers, type annotations.
func NearbyQualifiedNames(src []byte) []string {
	seen := map[string]struct{}{}
	for _, m := range qualifiedName.FindAll(src, -1) {
		seen[string(m)] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// bareIdent matches a single PascalCase/camelCase identifier, used as
// the fallback subject-inference signal when no qualified name fuzzy-
// matches the test name.
var bareIdent = regexp.MustCompile(`\b[A-Z][A-Za-z0-9_]*\b`)

// NearbyIdentifiers returns PascalCase identifiers observed in src,
// deduplicated, sorted, and capped at 64 entries to keep payloads
// bounded.
func NearbyIdentifiers(src []byte) []string {
	seen := map[string]struct{}{}
	for _, m := range bareIdent.FindAll(src, -1) {
		seen[string(m)] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// InferSubject runs the §5 heuristic: take the test name, strip the
// language-specific prefix ("Test" / "test_") and locate the
// qualified name that best matches it (case-insensitive substring on
// the trailing identifier). Returns the SelectorRef + confidence
// score in 0.6..0.8 per the contract; on failure returns the zero
// value and confidence 0 — the caller treats that as "no inference."
func InferSubject(testName string, src []byte) (code_framework.SelectorRef, float64) {
	target := stripTestPrefix(testName)
	if target == "" {
		return code_framework.SelectorRef{}, 0
	}
	low := strings.ToLower(target)

	bestName := ""
	bestScore := 0.0
	for _, qn := range NearbyQualifiedNames(src) {
		// Last segment is the most-specific identifier.
		seg := qn
		if i := strings.LastIndexByte(qn, '.'); i >= 0 {
			seg = qn[i+1:]
		}
		s := similarity(low, strings.ToLower(seg))
		if s > bestScore {
			bestScore = s
			bestName = qn
		}
	}
	if bestName != "" && bestScore >= 0.5 {
		// Map similarity (0.5-1.0) onto confidence (0.6-0.8) per
		// contract. Above-mid match → upper-band confidence.
		conf := 0.6 + 0.2*(bestScore-0.5)/0.5
		if conf > 0.8 {
			conf = 0.8
		}
		return code_framework.SelectorRef{
			Anchors: []code_framework.Anchor{{Kind: "qualified_name", Value: bestName}},
			Unique:  false,
		}, conf
	}

	// Bare-identifier fallback: same heuristic but on PascalCase
	// idents (only used when no qualified-name matched). Confidence
	// caps at 0.6.
	for _, id := range NearbyIdentifiers(src) {
		s := similarity(low, strings.ToLower(id))
		if s > bestScore {
			bestScore = s
			bestName = id
		}
	}
	if bestName != "" && bestScore >= 0.5 {
		return code_framework.SelectorRef{
			Anchors: []code_framework.Anchor{{Kind: "qualified_name", Value: bestName}},
			Unique:  false,
		}, 0.6
	}
	return code_framework.SelectorRef{}, 0
}

// stripTestPrefix removes the "Test" / "test_" / "it_" / "should_"
// prefixes language test frameworks use, then trims separator chars.
func stripTestPrefix(name string) string {
	trimmed := name
	prefixes := []string{"Test", "test_", "it_", "should_", "spec_"}
	for _, p := range prefixes {
		if strings.HasPrefix(trimmed, p) {
			trimmed = strings.TrimPrefix(trimmed, p)
			break
		}
	}
	trimmed = strings.TrimLeft(trimmed, "_-/ ")
	return trimmed
}

// similarity computes a simple substring/overlap score in 0..1:
// 1.0 on exact match, 0.8 on prefix or suffix, 0.6 on contained
// substring (in either direction), 0 otherwise. The caller uses
// >= 0.5 as the inference threshold.
func similarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1.0
	}
	if strings.HasPrefix(b, a) || strings.HasPrefix(a, b) {
		return 0.8
	}
	if strings.HasSuffix(b, a) || strings.HasSuffix(a, b) {
		return 0.75
	}
	if strings.Contains(b, a) || strings.Contains(a, b) {
		return 0.6
	}
	return 0
}

// ---- ContractTest matching --------------------------------------------------

// KnownFrameworkRows is the snapshot the contract-test matcher reads:
// every Event.event_name and Route.path_pattern present in
// code.framework at extract time. The lookup is best-effort against
// the workspace's current state.
type KnownFrameworkRows struct {
	EventTopics []string // Event.name values
	RoutePaths  []string // Route.path_pattern values
}

// LoadKnownRows queries the framework Facts handle for current Event
// and Route entities. Returns an empty struct (no error) if the layer
// has not produced any rows yet.
func LoadKnownRows(ctx context.Context, f facts.Facts) (KnownFrameworkRows, error) {
	var out KnownFrameworkRows
	if f == nil {
		return out, nil
	}
	env, err := f.ReadCurrent(ctx, kernel.LayerQuery{})
	if err != nil {
		// Caller treats query errors as "no rows" — ContractTest
		// emission is best-effort.
		return out, nil
	}
	if len(env.Data) == 0 {
		return out, nil
	}
	var events []kernel.Event
	if err := json.Unmarshal(env.Data, &events); err != nil {
		return out, nil
	}
	for _, ev := range events {
		if ev.Layer != "code.framework" {
			continue
		}
		switch ev.Kind {
		case "EventTopicObserved", "EventPublisherAdded", "EventSubscriberAdded":
			var p struct {
				Name      string `json:"name"`
				EventName string `json:"event_name"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err == nil {
				name := p.Name
				if name == "" {
					name = p.EventName
				}
				if name != "" {
					out.EventTopics = append(out.EventTopics, name)
				}
			}
		case "RouteAdded", "RouteChanged":
			var p struct {
				PathPattern string `json:"path_pattern"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err == nil && p.PathPattern != "" {
				out.RoutePaths = append(out.RoutePaths, p.PathPattern)
			}
		}
	}
	out.EventTopics = dedupSorted(out.EventTopics)
	out.RoutePaths = dedupSorted(out.RoutePaths)
	return out, nil
}

// ContractMatch is the result of scanning a test file's string
// literals against known framework rows.
type ContractMatch struct {
	TopicName string // non-empty if the literal matched an Event.name
	RoutePath string // non-empty if the literal matched a Route.path_pattern
}

// MatchContractTargets scans literals in src and returns any matches
// against rows. A single test file may match multiple targets; the
// caller emits one ContractTest per match.
func MatchContractTargets(src []byte, rows KnownFrameworkRows) []ContractMatch {
	if len(rows.EventTopics) == 0 && len(rows.RoutePaths) == 0 {
		return nil
	}
	topicSet := map[string]struct{}{}
	for _, t := range rows.EventTopics {
		topicSet[t] = struct{}{}
	}
	pathSet := map[string]struct{}{}
	for _, p := range rows.RoutePaths {
		pathSet[p] = struct{}{}
	}
	seen := map[string]struct{}{}
	var matches []ContractMatch
	for _, lit := range ExtractStringLiterals(src) {
		if _, ok := topicSet[lit]; ok {
			key := "topic:" + lit
			if _, dup := seen[key]; !dup {
				seen[key] = struct{}{}
				matches = append(matches, ContractMatch{TopicName: lit})
			}
		}
		if _, ok := pathSet[lit]; ok {
			key := "route:" + lit
			if _, dup := seen[key]; !dup {
				seen[key] = struct{}{}
				matches = append(matches, ContractMatch{RoutePath: lit})
			}
		}
	}
	return matches
}

// ---- Tiny utilities ---------------------------------------------------------

func dedupSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	for _, s := range in {
		seen[s] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// AnchorForPath builds a SelectorRef anchoring an entity at a file
// path. The selector is used as the AnchoredTo for Test/Fixture
// entities — the file itself is the anchor surface in code.core.
func AnchorForPath(path string) code_framework.SelectorRef {
	return code_framework.SelectorRef{
		Anchors: []code_framework.Anchor{{Kind: "path_glob", Value: path}},
		Unique:  false,
	}
}

// AnchorForFunction builds a SelectorRef anchoring an entity at a
// (path, qualified_name) pair — used as AnchoredTo for the test
// function itself.
func AnchorForFunction(path, qualifiedName string) code_framework.SelectorRef {
	anchors := []code_framework.Anchor{
		{Kind: "qualified_name", Value: qualifiedName},
	}
	if path != "" {
		anchors = append(anchors, code_framework.Anchor{Kind: "path_glob", Value: path})
	}
	return code_framework.SelectorRef{Anchors: anchors, Unique: true}
}

// MakeProvenance builds a per-fact Provenance row tagged with
// extractor:framework:<name>. Confidence is the caller's chosen
// score (subject inference confidence for Test, fixed 1.0 for the
// anchor itself).
func MakeProvenance(extractorName string, confidence float64, producedSeq uint64, inputs ...string) code_framework.Provenance {
	return code_framework.Provenance{
		Confidence:  confidence,
		Freshness:   kernel.FreshnessCurrent,
		SourceClass: []kernel.SourceClass{code_framework.SourceExtractorFramework},
		ProducedBy:  "extractor:framework:" + extractorName,
		ProducedSeq: producedSeq,
		Inputs:      inputs,
	}
}

// IsIdent reports whether r is a valid Go/TS/Py identifier character.
// Used in places where we need to skip past identifier runs without
// pulling in unicode tables for every language.
func IsIdent(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
