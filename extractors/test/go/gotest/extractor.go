// Package gotest implements the code.framework test-discovery
// extractor for Go's testing package. It detects `func TestXxx(t
// *testing.T)` declarations in `_test.go` files and the `t.Run(...)`
// sub-test scaffolding for table-driven tests, emitting one
// code_framework.Test per top-level test plus one per sub-case.
//
// Subject inference uses the §5 heuristic (nearby qualified-name
// references in the test file that fuzzy-match the test name);
// confidence stays in the 0.6..0.8 band.
//
// Contract-test discovery is best-effort: if the test source contains
// a string literal equal to a known event_name or path_pattern from
// code.framework, an additional ContractTest event is emitted. The
// match is intentionally simple (string equality) — typed event
// registries are post-v1 per plan §5 risks.
package gotest

import (
	"context"
	"regexp"
	"strings"

	"github.com/shivamstaq/graph-harness/extractors/test/common"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Name is the registry handle for this extractor.
const Name = "tests.go.gotest"

// goTestExtractor implements code_framework.Extractor.
type goTestExtractor struct {
	deps code_framework.Deps
}

// New constructs a goTestExtractor.
func New(deps code_framework.Deps) (code_framework.Extractor, error) {
	return &goTestExtractor{deps: deps}, nil
}

// Name returns the registry name.
func (e *goTestExtractor) Name() string { return Name }

// Inputs subscribes to code.core FileChanged events; that's enough to
// re-extract the test file's facts whenever it (or any sibling)
// changes on disk.
func (e *goTestExtractor) Inputs() []code_framework.EventKind {
	return []code_framework.EventKind{code_framework.InputCoreFileChanged}
}

// Outputs declares the entity kinds this extractor may emit.
func (e *goTestExtractor) Outputs() []code_framework.EntityKind {
	return []code_framework.EntityKind{
		code_framework.KindTest,
		code_framework.KindFixture,
		code_framework.KindContractTest,
	}
}

// Capabilities advertises framework coverage.
func (e *goTestExtractor) Capabilities() code_framework.Capabilities {
	return code_framework.Capabilities{
		Family:     "tests",
		Languages:  []string{"go"},
		Frameworks: []string{"go_test"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// Descriptor is the static descriptor used at Register-time. Exposed
// so the orchestrator's compile-time aggregator can pull it without
// instantiating the extractor.
func Descriptor() code_framework.Descriptor {
	return code_framework.Descriptor{
		Name:       Name,
		Family:     "tests",
		Languages:  []string{"go"},
		Frameworks: []string{"go_test"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     []code_framework.EventKind{code_framework.InputCoreFileChanged},
		Outputs: []code_framework.EntityKind{
			code_framework.KindTest,
			code_framework.KindFixture,
			code_framework.KindContractTest,
		},
	}
}

// OnEvent parses the changed file (best-effort) and returns one event
// per Test discovered, plus any ContractTest matches.
func (e *goTestExtractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	payload, err := common.DecodeFileChanged(in)
	if err != nil {
		return nil, nil //nolint:nilerr // best-effort
	}
	if !common.IsGoTestFile(payload.Path) {
		return nil, nil
	}

	src, _, err := common.ReadFile(e.deps.Workspace, payload.Path)
	if err != nil || len(src) == 0 {
		return nil, nil //nolint:nilerr
	}

	pkg := extractGoPackage(src)
	tests := findGoTests(src)
	if len(tests) == 0 {
		return nil, nil
	}

	rows, _ := common.LoadKnownRows(ctx, e.deps.Facts)
	contractMatches := common.MatchContractTargets(src, rows)

	out := make([]kernel.Event, 0, len(tests)+len(contractMatches))
	for _, t := range tests {
		qn := joinPkg(pkg, t.Name)
		anchor := common.AnchorForFunction(payload.Path, qn)
		subject, conf := common.InferSubject(t.Name, src)
		test := code_framework.Test{
			Name:       t.Name,
			Framework:  "go_test",
			SubjectRef: subject,
			AnchoredTo: anchor,
		}
		test.Provenance = common.MakeProvenance(Name, conf, in.Seq, payload.Path)
		test.ID = code_framework.MakeContentID(code_framework.KindTest, anchor, map[string]any{
			"name":      t.Name,
			"framework": "go_test",
			"path":      payload.Path,
		})
		out = append(out, common.EmitTest(test))

		// Sub-tests from t.Run("name", ...). Each emits its own Test
		// row with Name="<TestName>/<subcaseName>".
		for _, sub := range t.SubTests {
			subQN := qn + "/" + sub
			subAnchor := common.AnchorForFunction(payload.Path, subQN)
			subTest := code_framework.Test{
				Name:       t.Name + "/" + sub,
				Framework:  "go_test",
				SubjectRef: subject,
				AnchoredTo: subAnchor,
			}
			subTest.Provenance = common.MakeProvenance(Name, conf, in.Seq, payload.Path)
			subTest.ID = code_framework.MakeContentID(code_framework.KindTest, subAnchor, map[string]any{
				"name":      subTest.Name,
				"framework": "go_test",
				"path":      payload.Path,
				"parent":    t.Name,
			})
			out = append(out, common.EmitTest(subTest))
		}
	}

	// ContractTest emission — one row per matched (topic | path).
	// Anchor at the file (the test file as a whole exercises the
	// contract; per-test attribution is left to Pass-2 framework-edge
	// propagation which queries by file path).
	for _, cm := range contractMatches {
		anchor := common.AnchorForPath(payload.Path)
		ct := code_framework.ContractTest{
			TopicName:  cm.TopicName,
			AnchoredTo: anchor,
		}
		if cm.RoutePath != "" {
			ct.RouteRef = code_framework.SelectorRef{
				Anchors: []code_framework.Anchor{{Kind: "route_pattern", Value: cm.RoutePath}},
			}
		}
		ct.Provenance = common.MakeProvenance(Name, 0.7, in.Seq, payload.Path)
		ct.ID = code_framework.MakeContentID(code_framework.KindContractTest, anchor, map[string]any{
			"path":  payload.Path,
			"topic": cm.TopicName,
			"route": cm.RoutePath,
		})
		out = append(out, common.EmitContractTest(ct))
	}
	return out, nil
}

// goTestRow is the intermediate representation populated by
// findGoTests.
type goTestRow struct {
	Name     string
	SubTests []string
}

// goTestFunc matches `func TestXxx(t *testing.T)` declarations. The
// receiver clause is optional (Go test funcs are free functions, never
// methods, but we allow the receiver position for grammar tolerance —
// it just never matches in real test code).
var goTestFunc = regexp.MustCompile(`(?m)^func\s+(Test[A-Z][A-Za-z0-9_]*)\s*\(\s*[A-Za-z_][A-Za-z0-9_]*\s+\*testing\.T\s*\)`)

// tRunCall matches t.Run("name", ...) or t.Run(\`name\`, ...) — the
// table-driven sub-test idiom when the sub-case name is a literal.
var tRunCall = regexp.MustCompile("(?:t|tt|test|s|sub)\\.Run\\(\\s*[\"`]([^\"`]+)[\"`]")

// tRunIdentCall matches t.Run(identifier.field, ...) — the iterated
// table-driven idiom. The sub-case names are recovered separately
// from the table's struct-literal entries.
var tRunIdentCall = regexp.MustCompile(`(?:t|tt|test|s|sub)\.Run\(\s*([A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*)\s*,`)

// nameField captures a struct-literal entry `name: "value"` (with
// optional quote style). Used to recover the case names of an
// iterated t.Run table-driven test.
var nameField = regexp.MustCompile("(?:^|[\\s,{])name\\s*:\\s*[\"`]([^\"`]+)[\"`]")

// findGoTests scans src for top-level Test funcs and the t.Run
// sub-tests inside each. Returns one row per Test function with the
// (string-literal) sub-test names attached.
//
// Sub-test attribution: a t.Run call is attributed to the *most
// recent* Test func before its position in src. This is the simple
// scan model — it does not understand nested function bodies, so
// t.Run calls inside helper closures still attribute to the
// enclosing Test func, which matches go test's actual semantics
// (sub-tests run under whichever t they were spawned with).
func findGoTests(src []byte) []goTestRow {
	matches := goTestFunc.FindAllSubmatchIndex(src, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]goTestRow, len(matches))
	for i, m := range matches {
		name := string(src[m[2]:m[3]])
		out[i] = goTestRow{Name: name}
	}

	// Compute the body span for each Test function via brace-matching
	// from the first '{' after the func signature. Bodies don't
	// overlap (Go grammar), so each t.Run match belongs to exactly
	// one row.
	bodies := make([][2]int, len(matches))
	for i, m := range matches {
		start := findBodyStart(src, m[1])
		end := matchBrace(src, start)
		bodies[i] = [2]int{start, end}
	}

	// Literal-string t.Run: direct attribution.
	for _, sm := range tRunCall.FindAllSubmatchIndex(src, -1) {
		pos := sm[0]
		idx := attribute(bodies, pos)
		if idx < 0 {
			continue
		}
		sub := string(src[sm[2]:sm[3]])
		out[idx].SubTests = append(out[idx].SubTests, sub)
	}

	// Identifier-based t.Run (table-driven loops): if the enclosing
	// body contains `name: "X"` struct-literal entries, treat them as
	// the sub-case names. Coarse but matches the canonical Go test
	// table idiom.
	for _, sm := range tRunIdentCall.FindAllSubmatchIndex(src, -1) {
		pos := sm[0]
		idx := attribute(bodies, pos)
		if idx < 0 {
			continue
		}
		body := src[bodies[idx][0]:bodies[idx][1]]
		for _, nm := range nameField.FindAllSubmatch(body, -1) {
			out[idx].SubTests = append(out[idx].SubTests, string(nm[1]))
		}
	}

	for i := range out {
		out[i].SubTests = dedupStable(out[i].SubTests)
	}
	return out
}

// findBodyStart returns the index of the first '{' at or after pos
// (i.e. the open brace of the function body). Returns len(src) if
// there is none.
func findBodyStart(src []byte, pos int) int {
	for i := pos; i < len(src); i++ {
		if src[i] == '{' {
			return i
		}
	}
	return len(src)
}

// matchBrace returns the index immediately after the closing '}' of
// the brace at start. Tolerant of string literals and comments — we
// run a tiny state machine so braces inside strings don't throw the
// count off.
func matchBrace(src []byte, start int) int {
	if start >= len(src) || src[start] != '{' {
		return start
	}
	depth := 0
	i := start
	for i < len(src) {
		c := src[i]
		switch c {
		case '{':
			depth++
			i++
		case '}':
			depth--
			i++
			if depth == 0 {
				return i
			}
		case '"':
			i = skipGoString(src, i, '"')
		case '\'':
			i = skipGoString(src, i, '\'')
		case '`':
			i = skipGoRawString(src, i)
		case '/':
			if i+1 < len(src) && src[i+1] == '/' {
				for i < len(src) && src[i] != '\n' {
					i++
				}
			} else if i+1 < len(src) && src[i+1] == '*' {
				i += 2
				for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
					i++
				}
				i += 2
			} else {
				i++
			}
		default:
			i++
		}
	}
	return len(src)
}

// skipGoString advances past a Go interpreted string literal opened
// with q at src[i]. Handles backslash escapes.
func skipGoString(src []byte, i int, q byte) int {
	i++
	for i < len(src) {
		c := src[i]
		if c == '\\' {
			i += 2
			continue
		}
		if c == q || c == '\n' {
			return i + 1
		}
		i++
	}
	return len(src)
}

// skipGoRawString advances past a Go raw-string literal opened with
// a backtick at src[i]. No escapes.
func skipGoRawString(src []byte, i int) int {
	i++
	for i < len(src) {
		if src[i] == '`' {
			return i + 1
		}
		i++
	}
	return len(src)
}

// attribute returns the row index whose body span contains pos, or
// -1 if none.
func attribute(bodies [][2]int, pos int) int {
	for i, b := range bodies {
		if pos >= b[0] && pos < b[1] {
			return i
		}
	}
	return -1
}

// extractGoPackage returns the package declaration from src; empty
// string if none present (degraded test files still produce useful
// Test rows — the package qualifier is just a nicety on the
// qualified-name anchor).
var goPackageDecl = regexp.MustCompile(`(?m)^package\s+([A-Za-z_][A-Za-z0-9_]*)`)

func extractGoPackage(src []byte) string {
	m := goPackageDecl.FindSubmatch(src)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// joinPkg returns "pkg.Name" or "Name" if pkg is empty.
func joinPkg(pkg, name string) string {
	if pkg == "" {
		return name
	}
	return pkg + "." + name
}

// dedupStable drops duplicates from in, preserving first-seen order.
func dedupStable(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := in[:0]
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// stripWhitespace trims and collapses runs of whitespace to a single
// space — utility for the sub-test name normalizer.
func stripWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// init registers the extractor with the framework registry. The
// orchestrator's aggregator package imports this subdir for side
// effects.
func init() {
	code_framework.Register(Name, New, Descriptor())
}

// _ keeps the unused stripWhitespace from being dead-code stripped.
// It's exposed for future test-name normalization once sub-tests
// with embedded whitespace need cleanup; right now t.Run names are
// passed through verbatim per go test semantics.
var _ = stripWhitespace
