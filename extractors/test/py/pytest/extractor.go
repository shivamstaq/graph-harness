// Package pytest implements the code.framework test-discovery
// extractor for pytest. Detects `def test_xxx(...)` functions and
// `@pytest.fixture` decorated functions in files matching pytest's
// default collection patterns (test_*.py / *_test.py).
//
// Limitations:
//   - parametrize/parametrize_with_cases generate sub-tests at
//     collection time. v1 emits ONE Test row per `def test_xxx`,
//     not one per parametrize tuple. The decorator surface is
//     surfaced via the SubjectRef's nearby-qualified-names list
//     so downstream consumers can attribute coverage.
//   - Fixture scope (function / class / module / session) is not
//     captured in v1. Fixture entities exist primarily so flows
//     can reference them; the scope attribute lands when the
//     framework anchor predicate set grows in P3.
package pytest

import (
	"context"
	"regexp"
	"strings"

	"github.com/shivamstaq/graph-harness/extractors/test/common"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Name is the registry handle.
const Name = "tests.py.pytest"

type pytestExtractor struct {
	deps code_framework.Deps
}

// New constructs the extractor.
func New(deps code_framework.Deps) (code_framework.Extractor, error) {
	return &pytestExtractor{deps: deps}, nil
}

func (e *pytestExtractor) Name() string { return Name }

func (e *pytestExtractor) Inputs() []code_framework.EventKind {
	return []code_framework.EventKind{code_framework.InputCoreFileChanged}
}

func (e *pytestExtractor) Outputs() []code_framework.EntityKind {
	return []code_framework.EntityKind{
		code_framework.KindTest,
		code_framework.KindFixture,
		code_framework.KindContractTest,
	}
}

func (e *pytestExtractor) Capabilities() code_framework.Capabilities {
	return code_framework.Capabilities{
		Family:     "tests",
		Languages:  []string{"python"},
		Frameworks: []string{"pytest"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// Descriptor exposes the static descriptor for the aggregator.
func Descriptor() code_framework.Descriptor {
	return code_framework.Descriptor{
		Name:       Name,
		Family:     "tests",
		Languages:  []string{"python"},
		Frameworks: []string{"pytest"},
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

func (e *pytestExtractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	payload, err := common.DecodeFileChanged(in)
	if err != nil {
		return nil, nil //nolint:nilerr
	}
	if !common.IsPyTestFile(payload.Path) {
		return nil, nil
	}
	src, _, err := common.ReadFile(e.deps.Workspace, payload.Path)
	if err != nil || len(src) == 0 {
		return nil, nil //nolint:nilerr
	}

	tests := findPytestFunctions(src)
	fixtures := findPytestFixtures(src)
	if len(tests) == 0 && len(fixtures) == 0 {
		return nil, nil
	}

	rows, _ := common.LoadKnownRows(ctx, e.deps.Facts)
	contractMatches := common.MatchContractTargets(src, rows)

	out := make([]kernel.Event, 0, len(tests)+len(fixtures)+len(contractMatches))
	for _, name := range tests {
		qn := pyQualified(payload.Path, name)
		anchor := common.AnchorForFunction(payload.Path, qn)
		subject, conf := common.InferSubject(name, src)
		t := code_framework.Test{
			Name:       name,
			Framework:  "pytest",
			SubjectRef: subject,
			AnchoredTo: anchor,
		}
		t.Provenance = common.MakeProvenance(Name, conf, in.Seq, payload.Path)
		t.ID = code_framework.MakeContentID(code_framework.KindTest, anchor, map[string]any{
			"name":      name,
			"framework": "pytest",
			"path":      payload.Path,
		})
		out = append(out, common.EmitTest(t))
	}
	for _, name := range fixtures {
		qn := pyQualified(payload.Path, name)
		anchor := common.AnchorForFunction(payload.Path, qn)
		f := code_framework.Fixture{AnchoredTo: anchor}
		f.Provenance = common.MakeProvenance(Name, 0.8, in.Seq, payload.Path)
		f.ID = code_framework.MakeContentID(code_framework.KindFixture, anchor, map[string]any{
			"name": name,
			"path": payload.Path,
		})
		out = append(out, common.EmitFixture(f))
	}
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

// pytestFuncDecl matches `def test_xxx(...)` at any indent level —
// pytest collects test functions inside test classes (TestClass) the
// same way as top-level test_ functions. The leading-whitespace
// tolerance lets us pick up class-method tests with no extra logic.
var pytestFuncDecl = regexp.MustCompile(`(?m)^\s*def\s+(test_[A-Za-z0-9_]+)\s*\(`)

// pytestFixtureDecorator matches a fixture decorator on the line
// before a `def name(...)` — either `@pytest.fixture` or
// `@fixture` (when imported directly). Tolerant of decorator args:
// `@pytest.fixture(scope="module")`.
var pytestFixtureDecorator = regexp.MustCompile(`(?m)^\s*@(?:pytest\.)?fixture\b[^\n]*\n\s*def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

func findPytestFunctions(src []byte) []string {
	matches := pytestFuncDecl.FindAllSubmatch(src, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		name := string(m[1])
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func findPytestFixtures(src []byte) []string {
	matches := pytestFixtureDecorator.FindAllSubmatch(src, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		name := string(m[1])
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// pyQualified builds a "<module>.<name>" qualified name from the
// file path; mirrors source_live/parser_py.go's pyModuleName logic
// but kept local so the extractor doesn't import the parser.
func pyQualified(path, name string) string {
	// Strip the .py extension and convert / to . (best-effort —
	// Python's import path resolution depends on sys.path which we
	// don't have here).
	mod := strings.TrimSuffix(path, ".py")
	mod = strings.ReplaceAll(mod, "/", ".")
	mod = strings.ReplaceAll(mod, "\\", ".")
	mod = strings.Trim(mod, ".")
	if mod == "" {
		return name
	}
	return mod + "." + name
}

func init() {
	code_framework.Register(Name, New, Descriptor())
}
