// Package jest implements the code.framework test-discovery extractor
// for Jest test files (*.test.ts / *.spec.ts and the .js equivalents).
// Detection is regex-based over describe / it / test / beforeEach /
// afterEach call shapes, with a brace-balance pass to reconstruct
// describe nesting for full-name assembly.
//
// Jest vs Vitest: identical call surfaces. This extractor activates
// for files that either:
//  1. Match the test-file naming convention AND contain no `vitest`
//     import (the common case — most projects pick one of the two);
//  2. Contain an explicit `from 'jest'` / `from '@jest/globals'`
//     import.
//
// Vitest gets its own extractor (extractors/test/ts/vitest) so
// `Test.Framework` correctly attributes the testing tool. Files that
// import BOTH (rare migration scenario) emit from both extractors —
// the dispatcher's content-id-based dedup leaves the more-specific
// (vitest-importing) entry in place because it computes a different
// id when its Framework differs.
package jest

import (
	"bytes"
	"context"

	"github.com/shivamstaq/graph-harness/extractors/test/common"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Name is the registry handle.
const Name = "tests.ts.jest"

type jestExtractor struct {
	deps code_framework.Deps
}

// New constructs the extractor.
func New(deps code_framework.Deps) (code_framework.Extractor, error) {
	return &jestExtractor{deps: deps}, nil
}

func (e *jestExtractor) Name() string { return Name }

func (e *jestExtractor) Inputs() []code_framework.EventKind {
	return []code_framework.EventKind{code_framework.InputCoreFileChanged}
}

func (e *jestExtractor) Outputs() []code_framework.EntityKind {
	return []code_framework.EntityKind{
		code_framework.KindTest,
		code_framework.KindFixture,
		code_framework.KindContractTest,
	}
}

func (e *jestExtractor) Capabilities() code_framework.Capabilities {
	return code_framework.Capabilities{
		Family:     "tests",
		Languages:  []string{"typescript"},
		Frameworks: []string{"jest"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// Descriptor exposes the static descriptor for the aggregator.
func Descriptor() code_framework.Descriptor {
	return code_framework.Descriptor{
		Name:       Name,
		Family:     "tests",
		Languages:  []string{"typescript"},
		Frameworks: []string{"jest"},
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

func (e *jestExtractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	payload, err := common.DecodeFileChanged(in)
	if err != nil {
		return nil, nil //nolint:nilerr
	}
	if !common.IsTSTestFile(payload.Path) {
		return nil, nil
	}
	src, _, err := common.ReadFile(e.deps.Workspace, payload.Path)
	if err != nil || len(src) == 0 {
		return nil, nil //nolint:nilerr
	}
	if !looksLikeJest(src) {
		return nil, nil
	}

	tests, fixtures := common.ScanTSTests(src)
	if len(tests) == 0 && len(fixtures) == 0 {
		return nil, nil
	}

	rows, _ := common.LoadKnownRows(ctx, e.deps.Facts)
	contractMatches := common.MatchContractTargets(src, rows)

	out := make([]kernel.Event, 0, len(tests)+len(fixtures)+len(contractMatches))
	for _, t := range tests {
		anchor := common.AnchorForFunction(payload.Path, t.FullName)
		subject, conf := common.InferSubject(t.CaseName, src)
		test := code_framework.Test{
			Name:       t.FullName,
			Framework:  "jest",
			SubjectRef: subject,
			AnchoredTo: anchor,
		}
		test.Provenance = common.MakeProvenance(Name, conf, in.Seq, payload.Path)
		test.ID = code_framework.MakeContentID(code_framework.KindTest, anchor, map[string]any{
			"name":      t.FullName,
			"framework": "jest",
			"path":      payload.Path,
		})
		out = append(out, common.EmitTest(test))
	}
	for _, f := range fixtures {
		fxName := f.Hook
		if f.Outer != "" {
			fxName = f.Outer + "/" + f.Hook
		}
		anchor := common.AnchorForFunction(payload.Path, fxName)
		fixture := code_framework.Fixture{AnchoredTo: anchor}
		fixture.Provenance = common.MakeProvenance(Name, 0.7, in.Seq, payload.Path)
		fixture.ID = code_framework.MakeContentID(code_framework.KindFixture, anchor, map[string]any{
			"hook":  f.Hook,
			"outer": f.Outer,
			"path":  payload.Path,
		})
		out = append(out, common.EmitFixture(fixture))
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

// looksLikeJest returns true when the source either:
//   - imports from "jest" or "@jest/globals", OR
//   - does NOT import from "vitest" (Jest's globals — describe, it,
//     test, expect — are implicit when @types/jest is in scope, so
//     absence of vitest is the v1 disambiguator).
func looksLikeJest(src []byte) bool {
	if bytes.Contains(src, []byte("from 'jest'")) ||
		bytes.Contains(src, []byte(`from "jest"`)) ||
		bytes.Contains(src, []byte("from '@jest/globals'")) ||
		bytes.Contains(src, []byte(`from "@jest/globals"`)) {
		return true
	}
	if bytes.Contains(src, []byte("from 'vitest'")) || bytes.Contains(src, []byte(`from "vitest"`)) {
		return false
	}
	return true
}

func init() {
	code_framework.Register(Name, New, Descriptor())
}
