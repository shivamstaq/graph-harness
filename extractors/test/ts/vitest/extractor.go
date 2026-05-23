// Package vitest implements the code.framework test-discovery
// extractor for Vitest test files. Activates only when the file
// explicitly imports `vitest` — Jest is the default if no testing
// import is observed (see ../jest/extractor.go for the disambiguation
// contract).
//
// Detection uses the same describe / it / test / beforeEach surface
// scanner as Jest (shared in extractors/test/common/tsscan.go); the
// only differences are Capabilities.Frameworks and Test.Framework
// strings.
package vitest

import (
	"bytes"
	"context"

	"github.com/shivamstaq/graph-harness/extractors/test/common"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Name is the registry handle.
const Name = "tests.ts.vitest"

type vitestExtractor struct {
	deps code_framework.Deps
}

// New constructs the extractor.
func New(deps code_framework.Deps) (code_framework.Extractor, error) {
	return &vitestExtractor{deps: deps}, nil
}

func (e *vitestExtractor) Name() string { return Name }

func (e *vitestExtractor) Inputs() []code_framework.EventKind {
	return []code_framework.EventKind{code_framework.InputCoreFileChanged}
}

func (e *vitestExtractor) Outputs() []code_framework.EntityKind {
	return []code_framework.EntityKind{
		code_framework.KindTest,
		code_framework.KindFixture,
		code_framework.KindContractTest,
	}
}

func (e *vitestExtractor) Capabilities() code_framework.Capabilities {
	return code_framework.Capabilities{
		Family:     "tests",
		Languages:  []string{"typescript"},
		Frameworks: []string{"vitest"},
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
		Frameworks: []string{"vitest"},
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

func (e *vitestExtractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
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
	if !looksLikeVitest(src) {
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
			Framework:  "vitest",
			SubjectRef: subject,
			AnchoredTo: anchor,
		}
		test.Provenance = common.MakeProvenance(Name, conf, in.Seq, payload.Path)
		test.ID = code_framework.MakeContentID(code_framework.KindTest, anchor, map[string]any{
			"name":      t.FullName,
			"framework": "vitest",
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

// looksLikeVitest returns true only when the file explicitly imports
// from 'vitest'. Jest is the default for ambiguous files (see
// ../jest/extractor.go::looksLikeJest).
func looksLikeVitest(src []byte) bool {
	return bytes.Contains(src, []byte("from 'vitest'")) ||
		bytes.Contains(src, []byte(`from "vitest"`)) ||
		bytes.Contains(src, []byte("from 'vitest/globals'")) ||
		bytes.Contains(src, []byte(`from "vitest/globals"`))
}

func init() {
	code_framework.Register(Name, New, Descriptor())
}
