// Package gqlgen implements the graphql.go.gqlgen extractor.
// gqlgen produces a code-first GraphQL server by combining:
//   - `.graphql` / `.graphqls` schema files (the SDL source of truth)
//   - generated `*.resolvers.go` files with receiver methods on
//     `*queryResolver` / `*mutationResolver` / `*subscriptionResolver`.
//
// We treat both file shapes as inputs:
//
//   * A `.graphql` / `.graphqls` change → parse SDL via common.ParseSDL
//     and emit one GraphQLOperation per field. The resolver SelectorRef
//     points at the conventional method on the matching resolver type
//     (Query -> queryResolver.<Name>).
//
//   * A `*.resolvers.go` change → parse the file with the Go tree-sitter
//     grammar (shared with code.core), match every method whose receiver
//     names ends in `queryResolver`/`mutationResolver`/`subscriptionResolver`,
//     and emit one GraphQLOperation per such method.
package gqlgen

import (
	"context"
	"regexp"
	"strings"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/source_live"

	"github.com/shivamstaq/graph-harness/extractors/graphql/common"
)

const extractorName = "graphql.go.gqlgen"

var (
	inputs  = []cf.EventKind{cf.InputCoreFileChanged}
	outputs = []cf.EntityKind{cf.KindGraphQLOperation, cf.KindMutation}
)

type extractor struct{ deps cf.Deps }

// New is the public Constructor used by Register.
func New(deps cf.Deps) (cf.Extractor, error) { return &extractor{deps: deps}, nil }

func (e *extractor) Name() string             { return extractorName }
func (e *extractor) Inputs() []cf.EventKind   { return inputs }
func (e *extractor) Outputs() []cf.EntityKind { return outputs }
func (e *extractor) Capabilities() cf.Capabilities {
	return cf.Capabilities{
		Family:     "graphql",
		Languages:  []string{"go"},
		Frameworks: []string{"gqlgen"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerTx,
	}
}

func (e *extractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	path, _, ok := common.DecodeFileChanged(in)
	if !ok {
		return nil, nil
	}
	src, err := common.ReadFile(e.deps.Workspace, path)
	if err != nil || len(src) == 0 {
		return nil, err
	}
	ops, err := ExtractWithPath(path, src)
	if err != nil {
		return nil, err
	}
	return common.EmitOperations(ops)
}

// ExtractWithPath dispatches on file extension between the SDL
// parser and the Go resolver parser. Exported for tests.
func ExtractWithPath(path string, src []byte) ([]common.Operation, error) {
	switch {
	case strings.HasSuffix(path, ".graphql"), strings.HasSuffix(path, ".graphqls"):
		return extractSDL(path, string(src)), nil
	case strings.HasSuffix(path, ".resolvers.go"):
		return extractResolversGo(path, src)
	case strings.HasSuffix(path, ".go"):
		// Heuristic: only inspect when the file contains a resolver
		// receiver to avoid scanning unrelated .go files.
		if reResolverReceiver.Match(src) {
			return extractResolversGo(path, src)
		}
	}
	return nil, nil
}

func extractSDL(path, src string) []common.Operation {
	var ops []common.Operation
	for _, f := range common.ParseSDL(src) {
		resolverType := receiverFor(f.OperationType)
		qualified := resolverType + "." + capitalize(f.Name)
		ops = append(ops, common.Operation{
			OperationType: f.OperationType,
			Name:          f.Name,
			Arguments:     f.Arguments,
			ReturnType:    f.ReturnType,
			AnchoredTo:    common.BuildSchemaAnchor(f.Name, path),
			ResolverRef:   common.BuildResolverAnchor(qualified, path),
		})
	}
	return ops
}

// reResolverReceiver fingerprints whether a .go file is likely to
// contain gqlgen resolvers. We match the canonical generated form:
// `func (r *queryResolver) Foo(...)`.
var reResolverReceiver = regexp.MustCompile(`func\s*\(\s*\w+\s+\*?\s*(?:query|mutation|subscription)Resolver\s*\)`)

// reReceiverName captures the resolver-type substring from a method
// receiver: `(r *queryResolver)` → "queryResolver".
var reReceiverName = regexp.MustCompile(`\*?\s*(query|mutation|subscription)Resolver\b`)

func extractResolversGo(path string, src []byte) ([]common.Operation, error) {
	pf, err := source_live.ParseGoFile(path, src)
	if err != nil {
		return nil, err
	}
	var ops []common.Operation
	for _, fn := range pf.Functions {
		recv := fn.Receiver
		m := reReceiverName.FindStringSubmatch(recv)
		if m == nil {
			continue
		}
		opKind := strings.ToLower(m[1])
		name := lowerFirst(fn.Name)
		args := parseResolverArgs(fn.Signature)
		ret := parseResolverReturn(fn.Signature)
		qualified := fn.QualifiedName
		if qualified == "" {
			qualified = recv + "." + fn.Name
		}
		ops = append(ops, common.Operation{
			OperationType: opKind,
			Name:          name,
			Arguments:     args,
			ReturnType:    ret,
			AnchoredTo:    common.BuildSchemaAnchor(name, path),
			ResolverRef:   common.BuildResolverAnchor(qualified, path),
		})
	}
	return ops, nil
}

func receiverFor(opType string) string {
	switch opType {
	case "query":
		return "queryResolver"
	case "mutation":
		return "mutationResolver"
	case "subscription":
		return "subscriptionResolver"
	}
	return ""
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	if s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-32) + s[1:]
	}
	return s
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	if s[0] >= 'A' && s[0] <= 'Z' {
		return string(s[0]+32) + s[1:]
	}
	return s
}

// parseResolverArgs walks a Go signature like `(ctx context.Context,
// id string) (*model.User, error)` and returns the names of every
// argument except `ctx`.
func parseResolverArgs(sig string) []string {
	// First parenthesized group is the params.
	open := strings.IndexByte(sig, '(')
	if open < 0 {
		return nil
	}
	depth := 0
	close := -1
	for i := open; i < len(sig); i++ {
		switch sig[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				close = i
				break
			}
		}
		if close >= 0 {
			break
		}
	}
	if close <= open {
		return nil
	}
	paramList := sig[open+1 : close]
	var out []string
	seen := map[string]struct{}{}
	for _, raw := range splitTopLevel(paramList, ',') {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		// First whitespace-separated token is the name.
		fields := strings.Fields(raw)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if name == "ctx" || name == "_" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// parseResolverReturn returns the first return-type token: for
// `(*model.User, error)` we return "*model.User".
func parseResolverReturn(sig string) string {
	// Find the second parenthesized group (or single non-paren type).
	open1 := strings.IndexByte(sig, '(')
	if open1 < 0 {
		return ""
	}
	depth := 0
	close1 := -1
	for i := open1; i < len(sig); i++ {
		switch sig[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				close1 = i
				break
			}
		}
		if close1 >= 0 {
			break
		}
	}
	if close1 < 0 || close1+1 >= len(sig) {
		return ""
	}
	tail := strings.TrimSpace(sig[close1+1:])
	if strings.HasPrefix(tail, "(") {
		// Multi-return — take the first token inside.
		inner := tail[1:]
		if i := strings.IndexAny(inner, ",)"); i >= 0 {
			return strings.TrimSpace(inner[:i])
		}
	}
	// Single return type.
	return strings.Fields(tail)[0]
}

// splitTopLevel splits a comma-or-other-separator-delimited list,
// respecting parens / brackets / braces nesting.
func splitTopLevel(s string, sep byte) []string {
	var out []string
	depth := 0
	last := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case sep:
			if depth == 0 {
				out = append(out, s[last:i])
				last = i + 1
			}
		}
	}
	out = append(out, s[last:])
	return out
}

func init() {
	cf.Register(extractorName, New, cf.Descriptor{
		Family:     "graphql",
		Languages:  []string{"go"},
		Frameworks: []string{"gqlgen"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerTx,
		Inputs:     inputs,
		Outputs:    outputs,
	})
}
