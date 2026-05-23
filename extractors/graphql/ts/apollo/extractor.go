// Package apollo implements the graphql.ts.apollo extractor.
// Apollo Server (apollo-server, @apollo/server, apollo-server-express,
// etc.) uses the same SDL + resolver-map shape as graphql-js, so the
// extraction logic is shared via the package-level Extract function
// from graphqljs.
//
// The distinct purpose of this extractor is:
//   1. To register under "graphql.ts.apollo" so the CLI / status
//      surface can report Apollo-specific coverage.
//   2. To restrict detection to files that import from one of the
//      apollo-server family packages, which suppresses false-positive
//      emissions in graphql-js-only codebases where graphqljs already
//      ran (Pass-1 still allows both to emit; the Dispatcher's
//      content-id dedup collapses identical payloads per §6.21).
package apollo

import (
	"context"
	"strings"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"

	"github.com/shivamstaq/graph-harness/extractors/graphql/common"
	"github.com/shivamstaq/graph-harness/extractors/graphql/ts/graphqljs"
)

const extractorName = "graphql.ts.apollo"

var (
	inputs  = []cf.EventKind{cf.InputCoreFileChanged}
	outputs = []cf.EntityKind{cf.KindGraphQLOperation, cf.KindMutation}
)

type extractor struct {
	deps cf.Deps
}

// New is the public Constructor used by Register.
func New(deps cf.Deps) (cf.Extractor, error) { return &extractor{deps: deps}, nil }

func (e *extractor) Name() string             { return extractorName }
func (e *extractor) Inputs() []cf.EventKind   { return inputs }
func (e *extractor) Outputs() []cf.EntityKind { return outputs }
func (e *extractor) Capabilities() cf.Capabilities {
	return cf.Capabilities{
		Family:     "graphql",
		Languages:  []string{"typescript"},
		Frameworks: []string{"apollo-server", "@apollo/server", "apollo-server-express"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerTx,
	}
}

func (e *extractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	path, _, ok := common.DecodeFileChanged(in)
	if !ok {
		return nil, nil
	}
	if !isTSLike(path) {
		return nil, nil
	}
	src, err := common.ReadFile(e.deps.Workspace, path)
	if err != nil || len(src) == 0 {
		return nil, err
	}
	text := string(src)
	if !mentionsApollo(text) {
		return nil, nil
	}
	ops := graphqljs.Extract(path, text)
	return common.EmitOperations(ops)
}

// mentionsApollo returns true when the file imports / requires
// anything that looks like an apollo-server package. We use string
// containment (cheap) over regex; misses are acceptable per the
// "best-effort, supported on contribution" disclaimer.
func mentionsApollo(text string) bool {
	for _, marker := range []string{
		"apollo-server", "@apollo/server", "ApolloServer",
		"apollo-server-express", "apollo-server-lambda",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func isTSLike(path string) bool {
	switch strings.ToLower(lastExt(path)) {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return true
	}
	return false
}

func lastExt(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '.' {
			return p[i:]
		}
		if p[i] == '/' || p[i] == '\\' {
			return ""
		}
	}
	return ""
}

func init() {
	cf.Register(extractorName, New, cf.Descriptor{
		Family:     "graphql",
		Languages:  []string{"typescript"},
		Frameworks: []string{"apollo-server", "@apollo/server"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerTx,
		Inputs:     inputs,
		Outputs:    outputs,
	})
}
