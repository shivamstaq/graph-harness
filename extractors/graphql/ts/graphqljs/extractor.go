// Package graphqljs implements the graphql.ts.graphqljs extractor.
// It targets the graphql-js / graphql-tools school of schema
// definition: SDL shipped via a `gql\`...\`` template literal,
// resolver maps shaped like `{ Query: { foo: () => ... } }`.
//
// Detection strategy:
//  1. Collect every `gql\`...\`` (or `graphql\`...\``) template
//     literal in the changed file, parse the SDL inside for
//     Query/Mutation/Subscription fields, and emit one
//     GraphQLOperation per field.
//  2. Independently, walk the file text for resolver-map literals
//     `Query: { name(...) }` / `Mutation: { ... }` /
//     `Subscription: { ... }` blocks and emit one
//     GraphQLOperation per detected resolver. Resolver-only
//     operations (no SDL match in this file) still emit; the
//     resolver SelectorRef points at the implementing function.
package graphqljs

import (
	"context"
	"regexp"
	"strings"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"

	"github.com/shivamstaq/graph-harness/extractors/graphql/common"
)

const extractorName = "graphql.ts.graphqljs"

// inputs/outputs are exported so the static descriptor in init()
// can declare the same values OnEvent / Outputs() return.
var (
	inputs  = []cf.EventKind{cf.InputCoreFileChanged}
	outputs = []cf.EntityKind{cf.KindGraphQLOperation, cf.KindMutation}
)

type extractor struct {
	deps cf.Deps
}

// New is the public Constructor used by Register.
func New(deps cf.Deps) (cf.Extractor, error) { return &extractor{deps: deps}, nil }

func (e *extractor) Name() string                { return extractorName }
func (e *extractor) Inputs() []cf.EventKind      { return inputs }
func (e *extractor) Outputs() []cf.EntityKind    { return outputs }
func (e *extractor) Capabilities() cf.Capabilities {
	return cf.Capabilities{
		Family:     "graphql",
		Languages:  []string{"typescript"},
		Frameworks: []string{"graphql-js", "graphql-tools"},
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

	// Fast reject: must mention gql/graphql template literal OR a
	// Query/Mutation/Subscription resolver-map key.
	text := string(src)
	if !strings.Contains(text, "gql`") && !strings.Contains(text, "graphql`") &&
		!resolverMapPresent(text) {
		return nil, nil
	}

	ops := Extract(path, text)
	return common.EmitOperations(ops)
}

// Extract is the package-level pure function exposed for tests and
// for the eventual cross-extractor reuse (Apollo's extractor calls
// into the same SDL+resolver-map detection).
func Extract(path, src string) []common.Operation {
	var ops []common.Operation
	seen := map[string]struct{}{} // dedupe by (op, name) inside one file

	for _, block := range common.ExtractGqlBlocks(src) {
		for _, f := range common.ParseSDL(block) {
			key := f.OperationType + ":" + f.Name
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			ops = append(ops, buildOp(path, f, ""))
		}
	}
	for _, r := range scanResolverMap(src) {
		key := r.op + ":" + r.name
		if _, dup := seen[key]; dup {
			// Upgrade the existing op's ResolverRef with the
			// resolver target we just found.
			for i := range ops {
				if ops[i].OperationType == r.op && ops[i].Name == r.name && len(ops[i].ResolverRef.Anchors) == 0 {
					ops[i].ResolverRef = common.BuildResolverAnchor(r.qualified, path)
				}
			}
			continue
		}
		seen[key] = struct{}{}
		ops = append(ops, buildOp(path, common.SDLField{
			OperationType: r.op,
			Name:          r.name,
		}, r.qualified))
	}
	return ops
}

func buildOp(path string, f common.SDLField, resolverQualified string) common.Operation {
	anchor := common.BuildSchemaAnchor(f.Name, path)
	var resolver cf.SelectorRef
	if resolverQualified != "" {
		resolver = common.BuildResolverAnchor(resolverQualified, path)
	} else {
		resolver = common.BuildResolverAnchor(f.Name, path)
	}
	return common.Operation{
		OperationType: f.OperationType,
		Name:          f.Name,
		Arguments:     f.Arguments,
		ReturnType:    f.ReturnType,
		AnchoredTo:    anchor,
		ResolverRef:   resolver,
	}
}

// ---- resolver-map detection ----------------------------------------

type resolver struct {
	op        string // "query"|"mutation"|"subscription"
	name      string
	qualified string // best-effort: ModulePrefix.opName
}

// reResolverBlock looks for `Query: { ... }` style resolver maps
// nested inside an object literal. We allow nested braces by
// using a greedy-but-bounded matcher: capture from `Query: {` to
// the next matching `}` at the same nesting level via the
// matchBalanced helper.
var reResolverHeader = regexp.MustCompile(`\b(Query|Mutation|Subscription)\s*:\s*\{`)

func resolverMapPresent(src string) bool {
	return reResolverHeader.MatchString(src)
}

func scanResolverMap(src string) []resolver {
	var out []resolver
	for _, m := range reResolverHeader.FindAllStringSubmatchIndex(src, -1) {
		op := toOpKind(src[m[2]:m[3]])
		// m[1] = index immediately after the opening `{`
		body, ok := matchBalanced(src, m[1]-1)
		if !ok {
			continue
		}
		for _, r := range scanResolverFields(body) {
			// Bare resolver name as the qualified-name —
			// graphql-js / Apollo don't carry a class context.
			// Cross-file resolution still works because the
			// resolver SelectorRef carries the path_glob anchor.
			out = append(out, resolver{op: op, name: r, qualified: r})
		}
	}
	return out
}

// matchBalanced reads from src[start] (which must be '{') and
// returns the contents between the braces (exclusive) once the
// nesting balances. ok=false when the file is malformed.
func matchBalanced(src string, start int) (string, bool) {
	if start < 0 || start >= len(src) || src[start] != '{' {
		return "", false
	}
	depth := 0
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start+1 : i], true
			}
		}
	}
	return "", false
}

// scanResolverFields finds the top-level property keys of a
// resolver-map block. We iterate over the block character-by-character
// tracking brace/paren/bracket nesting and only consider identifier
// tokens that appear at depth==0 followed (after whitespace) by `:`,
// `(`, or `,`. This rules out identifiers buried inside arrow-function
// bodies that are nested calls of unrelated helpers.
func scanResolverFields(block string) []string {
	var out []string
	seen := map[string]struct{}{}
	reserved := map[string]struct{}{
		"async": {}, "function": {}, "return": {}, "true": {},
		"false": {}, "null": {}, "undefined": {}, "if": {}, "else": {},
	}
	depth := 0
	i := 0
	atKeyPosition := true // entering the block, the first ident is a key
	for i < len(block) {
		c := block[i]
		switch {
		case c == '{' || c == '(' || c == '[':
			depth++
			i++
		case c == '}' || c == ')' || c == ']':
			depth--
			if depth < 0 {
				depth = 0
			}
			i++
		case c == ',' && depth == 0:
			atKeyPosition = true
			i++
		case c == '/' && i+1 < len(block) && block[i+1] == '/':
			// Line comment.
			for i < len(block) && block[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(block) && block[i+1] == '*':
			// Block comment.
			i += 2
			for i+1 < len(block) && !(block[i] == '*' && block[i+1] == '/') {
				i++
			}
			if i+1 < len(block) {
				i += 2
			}
		case isIdentStart(c):
			if depth != 0 || !atKeyPosition {
				// Skip the identifier token regardless.
				j := i
				for j < len(block) && isIdentPart(block[j]) {
					j++
				}
				i = j
				continue
			}
			j := i
			for j < len(block) && isIdentPart(block[j]) {
				j++
			}
			name := block[i:j]
			i = j
			// Look ahead for `:` or `(` after optional whitespace.
			k := i
			for k < len(block) && (block[k] == ' ' || block[k] == '\t' || block[k] == '\n' || block[k] == '\r') {
				k++
			}
			if k < len(block) && (block[k] == ':' || block[k] == '(') {
				if _, bad := reserved[name]; !bad {
					if _, dup := seen[name]; !dup {
						seen[name] = struct{}{}
						out = append(out, name)
					}
				}
			}
			atKeyPosition = false
		default:
			i++
		}
	}
	return out
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '$'
}
func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func toOpKind(typeName string) string {
	switch typeName {
	case "Query":
		return "query"
	case "Mutation":
		return "mutation"
	case "Subscription":
		return "subscription"
	}
	return ""
}

// ---- shared helpers ------------------------------------------------

func isTSLike(path string) bool {
	switch ext := strings.ToLower(lastExt(path)); ext {
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
		Frameworks: []string{"graphql-js", "graphql-tools"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerTx,
		Inputs:     inputs,
		Outputs:    outputs,
	})
}
