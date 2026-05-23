// Package ariadne implements the graphql.py.ariadne extractor.
// Ariadne is a Python GraphQL framework whose primary idioms are:
//
//	type_defs = gql("type Query { user(id: ID!): User }")
//	query = QueryType()
//	@query.field("user")
//	def resolve_user(_, info, id):
//	    return find_user(id)
//
//	mutation = MutationType()
//	mutation.set_field("createUser", resolve_create_user)
//
// We emit GraphQLOperation rows for:
//   * Every Query/Mutation/Subscription field declared in any
//     `gql("type ... { ... }")` / triple-string assignment.
//   * Every `<obj>.set_field("name", resolver)` and `@<obj>.field("name")`
//     call paired with the matching `Query|Mutation|Subscription` type.
package ariadne

import (
	"context"
	"regexp"
	"strings"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"

	"github.com/shivamstaq/graph-harness/extractors/graphql/common"
)

const extractorName = "graphql.py.ariadne"

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
		Languages:  []string{"python"},
		Frameworks: []string{"ariadne"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerTx,
	}
}

func (e *extractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	path, _, ok := common.DecodeFileChanged(in)
	if !ok {
		return nil, nil
	}
	if !strings.HasSuffix(strings.ToLower(path), ".py") {
		return nil, nil
	}
	src, err := common.ReadFile(e.deps.Workspace, path)
	if err != nil || len(src) == 0 {
		return nil, err
	}
	text := string(src)
	if !strings.Contains(text, "ariadne") && !strings.Contains(text, "QueryType") &&
		!strings.Contains(text, "MutationType") {
		return nil, nil
	}
	ops := Extract(path, text)
	return common.EmitOperations(ops)
}

// reTypeBinding finds variable bindings to QueryType / MutationType /
// SubscriptionType: `query = QueryType()`.
var reTypeBinding = regexp.MustCompile(`(?m)^([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(QueryType|MutationType|SubscriptionType)\s*\(`)

// reSetField matches `var.set_field("name", resolver)`.
var reSetField = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\.set_field\(\s*["']([^"']+)["']\s*,\s*([A-Za-z_][A-Za-z0-9_]*)`)

// reFieldDecorator matches `@var.field("name")`.
var reFieldDecorator = regexp.MustCompile(`@([A-Za-z_][A-Za-z0-9_]*)\.field\(\s*["']([^"']+)["']\s*\)`)

// reTripleStringSDL captures SDL declared inside a triple-quoted
// Python string. We pick up the contents between the triple quotes
// only — the surrounding `gql(...)` wrapper is ignored.
var reTripleStringSDL = regexp.MustCompile(`(?s)"""(.*?)"""|'''(.*?)'''`)

// Extract is the pure routine exposed for tests.
func Extract(path, src string) []common.Operation {
	var ops []common.Operation
	bindings := map[string]string{} // var name → "query"|"mutation"|"subscription"
	for _, m := range reTypeBinding.FindAllStringSubmatch(src, -1) {
		bindings[m[1]] = strings.ToLower(strings.TrimSuffix(m[2], "Type"))
	}

	seen := map[string]struct{}{}

	// 1. SDL fields from triple-quoted strings.
	for _, m := range reTripleStringSDL.FindAllStringSubmatch(src, -1) {
		block := m[1]
		if block == "" {
			block = m[2]
		}
		for _, f := range common.ParseSDL(block) {
			key := f.OperationType + ":" + f.Name
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			ops = append(ops, common.Operation{
				OperationType: f.OperationType,
				Name:          f.Name,
				Arguments:     f.Arguments,
				ReturnType:    f.ReturnType,
				AnchoredTo:    common.BuildSchemaAnchor(f.Name, path),
				ResolverRef:   common.BuildResolverAnchor("resolve_"+f.Name, path),
			})
		}
	}

	// 2. set_field bindings.
	for _, m := range reSetField.FindAllStringSubmatch(src, -1) {
		variable := m[1]
		fieldName := m[2]
		resolver := m[3]
		op, ok := bindings[variable]
		if !ok {
			continue
		}
		key := op + ":" + fieldName
		if _, dup := seen[key]; dup {
			updateResolver(ops, op, fieldName, resolver, path)
			continue
		}
		seen[key] = struct{}{}
		ops = append(ops, common.Operation{
			OperationType: op,
			Name:          fieldName,
			AnchoredTo:    common.BuildSchemaAnchor(fieldName, path),
			ResolverRef:   common.BuildResolverAnchor(resolver, path),
		})
	}

	// 3. @<var>.field("name") decorators paired with the next def.
	for _, m := range reFieldDecorator.FindAllStringSubmatchIndex(src, -1) {
		variable := src[m[2]:m[3]]
		fieldName := src[m[4]:m[5]]
		op, ok := bindings[variable]
		if !ok {
			continue
		}
		// Find the next `def name(...)` after the decorator.
		rest := src[m[1]:]
		nameMatch := reNextDef.FindStringSubmatch(rest)
		resolver := "resolve_" + fieldName
		if nameMatch != nil {
			resolver = nameMatch[1]
		}
		key := op + ":" + fieldName
		if _, dup := seen[key]; dup {
			updateResolver(ops, op, fieldName, resolver, path)
			continue
		}
		seen[key] = struct{}{}
		ops = append(ops, common.Operation{
			OperationType: op,
			Name:          fieldName,
			AnchoredTo:    common.BuildSchemaAnchor(fieldName, path),
			ResolverRef:   common.BuildResolverAnchor(resolver, path),
		})
	}
	return ops
}

var reNextDef = regexp.MustCompile(`(?m)^\s*(?:async\s+)?def\s+([A-Za-z_][A-Za-z0-9_]*)`)

func updateResolver(ops []common.Operation, opKind, fieldName, resolver, path string) {
	for i := range ops {
		if ops[i].OperationType == opKind && ops[i].Name == fieldName {
			ops[i].ResolverRef = common.BuildResolverAnchor(resolver, path)
			return
		}
	}
}

func init() {
	cf.Register(extractorName, New, cf.Descriptor{
		Family:     "graphql",
		Languages:  []string{"python"},
		Frameworks: []string{"ariadne"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerTx,
		Inputs:     inputs,
		Outputs:    outputs,
	})
}
