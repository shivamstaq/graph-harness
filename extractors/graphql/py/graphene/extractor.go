// Package graphene implements the graphql.py.graphene extractor.
// Graphene is a Python GraphQL framework that exposes operations as
// class attributes on a `graphene.ObjectType` subclass:
//
//	class Query(graphene.ObjectType):
//	    user = graphene.Field(UserType, id=graphene.ID())
//	    def resolve_user(self, info, id):
//	        return find_user(id)
//
//	class CreateUser(graphene.Mutation):
//	    class Arguments:
//	        name = graphene.String(required=True)
//	    user = graphene.Field(UserType)
//	    def mutate(self, info, name):
//	        return CreateUser(user=User(name=name))
//
//	class Mutation(graphene.ObjectType):
//	    create_user = CreateUser.Field()
//
// We emit one GraphQLOperation per field/Mutation-subclass and bind
// the resolver SelectorRef to the matching `resolve_<name>` method
// when present (Query/Subscription) or the `mutate` method (Mutation).
package graphene

import (
	"context"
	"regexp"
	"strings"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"

	"github.com/shivamstaq/graph-harness/extractors/graphql/common"
)

const extractorName = "graphql.py.graphene"

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
		Frameworks: []string{"graphene"},
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
	if !strings.Contains(text, "graphene") {
		return nil, nil
	}
	ops := Extract(path, text)
	return common.EmitOperations(ops)
}

// reClassDecl finds `class Name(parents...):` headers and captures
// name + parents.
var reClassDecl = regexp.MustCompile(`(?m)^class\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:\(([^)]*)\))?\s*:`)

// reFieldAssign captures `name = graphene.X(...)` or similar
// `name = <RHS>` lines at indentation > 0. We require the RHS to
// mention `graphene.` so we don't mis-classify arbitrary class
// attributes.
var reFieldAssign = regexp.MustCompile(`(?m)^[\t ]+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*([^\n]*graphene\.[^\n]*)`)

// reResolveDef matches `def resolve_<name>(...)`.
var reResolveDef = regexp.MustCompile(`(?m)^[\t ]+def\s+resolve_([A-Za-z_][A-Za-z0-9_]*)\s*\(([^)]*)\)`)

// reMutateDef matches `def mutate(...)` inside a graphene.Mutation
// subclass.
var reMutateDef = regexp.MustCompile(`(?m)^[\t ]+def\s+mutate\s*\(([^)]*)\)`)

// Extract is the pure routine exposed for tests.
func Extract(path, src string) []common.Operation {
	var ops []common.Operation
	classes := findClassSpans(src)
	for _, c := range classes {
		op := classifyClass(c)
		if op == "" && !strings.Contains(c.parents, "Mutation") {
			continue
		}
		body := src[c.bodyStart:c.endByte]
		// Mutation subclass: emit one op for the class itself.
		if strings.Contains(c.parents, "graphene.Mutation") || strings.Contains(c.parents, "Mutation") && op == "" {
			name := snakeCase(c.name)
			args := mutationArgNames(body)
			resolver := c.name + ".mutate"
			ops = append(ops, common.Operation{
				OperationType: "mutation",
				Name:          name,
				Arguments:     args,
				ReturnType:    c.name,
				AnchoredTo:    common.BuildSchemaAnchor(name, path),
				ResolverRef:   common.BuildResolverAnchor(resolver, path),
			})
			continue
		}
		// Query / Subscription ObjectType: one op per field assignment.
		for _, m := range reFieldAssign.FindAllStringSubmatchIndex(body, -1) {
			name := body[m[2]:m[3]]
			rhs := body[m[4]:m[5]]
			ret := extractFieldType(rhs)
			args := extractFieldArgs(rhs)
			resolver := c.name + ".resolve_" + name
			ops = append(ops, common.Operation{
				OperationType: op,
				Name:          name,
				Arguments:     args,
				ReturnType:    ret,
				AnchoredTo:    common.BuildSchemaAnchor(name, path),
				ResolverRef:   common.BuildResolverAnchor(resolver, path),
			})
		}
	}
	return ops
}

func classifyClass(c classSpan) string {
	if !strings.Contains(c.parents, "graphene.ObjectType") && !strings.Contains(c.parents, "ObjectType") {
		return ""
	}
	switch c.name {
	case "Query":
		return "query"
	case "Subscription":
		return "subscription"
	case "Mutation":
		// Mutation that aggregates submutations — emitted by the
		// submutation classes themselves; the aggregator has no
		// fresh op.
		return ""
	}
	return ""
}

func mutationArgNames(body string) []string {
	// Look for `class Arguments:` and then field assignments
	// underneath. If absent, fall back to the `mutate` signature.
	if i := strings.Index(body, "class Arguments"); i >= 0 {
		end := classBodyEnd(body, i)
		argsBlock := body[i:end]
		var out []string
		seen := map[string]struct{}{}
		for _, m := range reFieldAssign.FindAllStringSubmatch(argsBlock, -1) {
			name := m[1]
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
		if len(out) > 0 {
			return out
		}
	}
	if m := reMutateDef.FindStringSubmatch(body); m != nil {
		return splitPyArgs(m[1])
	}
	return nil
}

func splitPyArgs(argList string) []string {
	if strings.TrimSpace(argList) == "" {
		return nil
	}
	skip := map[string]bool{"self": true, "cls": true, "root": true, "info": true}
	var out []string
	seen := map[string]struct{}{}
	for _, raw := range strings.Split(argList, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		// Strip type annotation / default value.
		for _, sep := range []string{":", "="} {
			if i := strings.Index(name, sep); i >= 0 {
				name = strings.TrimSpace(name[:i])
			}
		}
		if name == "" || skip[name] {
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

// extractFieldType pulls the first identifier inside a graphene
// field call: `graphene.Field(UserType, ...)` → "UserType",
// `graphene.String(required=True)` → "String".
var reFieldType = regexp.MustCompile(`graphene\.(?:Field|List|NonNull)?\(?\s*([A-Za-z_][A-Za-z0-9_]*)`)

func extractFieldType(rhs string) string {
	if m := reFieldType.FindStringSubmatch(rhs); m != nil {
		// Catch graphene.String / .Int / .ID directly.
		if strings.HasPrefix(strings.TrimSpace(rhs), "graphene.") && !strings.Contains(rhs, "graphene.Field") &&
			!strings.Contains(rhs, "graphene.List") && !strings.Contains(rhs, "graphene.NonNull") {
			// e.g. "graphene.String(required=True)" — capture
			// the after-dot name.
			r := strings.TrimSpace(rhs)
			r = strings.TrimPrefix(r, "graphene.")
			if i := strings.IndexAny(r, "(,\n "); i >= 0 {
				r = r[:i]
			}
			return r
		}
		return m[1]
	}
	return ""
}

// extractFieldArgs pulls argument names from a graphene field call's
// keyword-argument list: `graphene.String(id=graphene.ID())` →
// ["id"]. Heuristic.
var reKw = regexp.MustCompile(`,\s*([A-Za-z_][A-Za-z0-9_]*)\s*=`)

func extractFieldArgs(rhs string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, m := range reKw.FindAllStringSubmatch(rhs, -1) {
		name := m[1]
		// Skip well-known meta kwargs.
		if name == "required" || name == "description" || name == "default_value" {
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

func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r + 32)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

type classSpan struct {
	name      string
	parents   string
	headerByte int
	bodyStart  int
	endByte    int
}

func findClassSpans(src string) []classSpan {
	var out []classSpan
	for _, m := range reClassDecl.FindAllStringSubmatchIndex(src, -1) {
		name := src[m[2]:m[3]]
		parents := ""
		if m[4] >= 0 {
			parents = src[m[4]:m[5]]
		}
		header := m[0]
		eol := strings.IndexByte(src[header:], '\n')
		if eol < 0 {
			out = append(out, classSpan{name: name, parents: parents, headerByte: header, bodyStart: len(src), endByte: len(src)})
			continue
		}
		bodyStart := header + eol + 1
		end := classBodyEnd(src, bodyStart)
		out = append(out, classSpan{name: name, parents: parents, headerByte: header, bodyStart: bodyStart, endByte: end})
	}
	return out
}

func classBodyEnd(src string, bodyStart int) int {
	i := bodyStart
	for i < len(src) {
		lineStart := i
		eol := strings.IndexByte(src[i:], '\n')
		if eol < 0 {
			return len(src)
		}
		line := src[i : i+eol]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			i += eol + 1
			continue
		}
		if !(line[0] == ' ' || line[0] == '\t') {
			return lineStart
		}
		i += eol + 1
	}
	return len(src)
}

func init() {
	cf.Register(extractorName, New, cf.Descriptor{
		Family:     "graphql",
		Languages:  []string{"python"},
		Frameworks: []string{"graphene"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerTx,
		Inputs:     inputs,
		Outputs:    outputs,
	})
}
