// Package typegraphql implements the graphql.ts.typegraphql extractor.
// type-graphql is a class-decorator-based GraphQL framework: methods
// of a class decorated with @Resolver(...) become operations when
// individually annotated with @Query() / @Mutation() / @Subscription().
//
// Detection strategy is a text scan rather than a full TS AST walk:
//   1. Locate every @Query / @Mutation / @Subscription decorator
//      call (possibly with parens) at line-start (modulo whitespace).
//   2. The next method declaration (`name(args): ReturnType`) on a
//      subsequent line is the bound resolver.
//   3. The enclosing class (the nearest preceding
//      `class Foo extends/implements/{`) is the qualified-name
//      prefix.
//
// This loses some accuracy compared to a true AST pass — comments
// and decorator-equipped non-method members can confuse us — but
// stays within the Pass-1 budget and degrades to lower-confidence
// without crashing on novel patterns.
package typegraphql

import (
	"context"
	"regexp"
	"strings"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"

	"github.com/shivamstaq/graph-harness/extractors/graphql/common"
)

const extractorName = "graphql.ts.typegraphql"

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
		Frameworks: []string{"type-graphql"},
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
	if !strings.Contains(text, "type-graphql") && !strings.Contains(text, "@Resolver") &&
		!strings.Contains(text, "@Query") && !strings.Contains(text, "@Mutation") {
		return nil, nil
	}
	ops := Extract(path, text)
	return common.EmitOperations(ops)
}

// reDecorator matches the start of an operation decorator
// (`@Query`, `@Mutation`, `@Subscription`). The parenthesized argument
// list (which may contain nested parens / line breaks) is captured
// separately by scanBalancedParen below.
var reDecorator = regexp.MustCompile(`@(Query|Mutation|Subscription)\b`)

// reMethodName matches the next method-declaration head following a
// decorator. We capture only the method name; the parenthesized
// argument list and return type are extracted with balanced-paren
// scanning to support nested decorators (`@Arg('x')`) and complex
// generic return types.
var reMethodName = regexp.MustCompile(`(?m)^[\t ]*(?:public\s+|private\s+|protected\s+|async\s+|static\s+)*([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// reClassDecl captures the enclosing class name. Used to seed the
// qualified-name prefix for resolver SelectorRefs.
var reClassDecl = regexp.MustCompile(`(?m)^[\t ]*(?:export\s+)?(?:abstract\s+)?class\s+([A-Za-z_][A-Za-z0-9_]*)`)

// reReturnHint extracts `() => User` style return-type hints from
// decorator arguments. The captured group is the return-type string.
var reReturnHint = regexp.MustCompile(`\(\s*\)\s*=>\s*([A-Za-z_][A-Za-z0-9_$<>\[\],\s|!]*)`)

// reArgName captures one parameter name from a TS method signature.
var reArgName = regexp.MustCompile(`(?:^|,)\s*(?:@\w+\s*\([^)]*\)\s*)?([A-Za-z_$][A-Za-z0-9_$]*)\s*[?:]`)

// Extract is the pure text-scanning routine exposed for tests.
func Extract(path, src string) []common.Operation {
	var ops []common.Operation
	classes := findClassSpans(src)
	seen := map[int]struct{}{} // method-name byte offset → dedup
	for _, m := range reDecorator.FindAllStringSubmatchIndex(src, -1) {
		decKind := src[m[2]:m[3]]
		// Decorator args (optional). Position immediately after the
		// decorator name; consume balanced parens if present.
		afterName := m[1]
		decArgs := ""
		if afterName < len(src) && src[afterName] == '(' {
			argsEnd, ok := scanBalancedParen(src, afterName)
			if ok {
				decArgs = src[afterName+1 : argsEnd]
				afterName = argsEnd + 1
			}
		}
		// Find the next method-name + paren signature.
		mm := reMethodName.FindStringSubmatchIndex(src[afterName:])
		if mm == nil {
			continue
		}
		nameStart := afterName + mm[2]
		nameEnd := afterName + mm[3]
		parenStart := afterName + mm[1] - 1 // mm[1] is end-of-match (after `(`)
		if _, dup := seen[nameStart]; dup {
			continue
		}
		seen[nameStart] = struct{}{}
		argsEnd, ok := scanBalancedParen(src, parenStart)
		if !ok {
			continue
		}
		argList := src[parenStart+1 : argsEnd]
		// Optional `: ReturnType` between `)` and `{`.
		rest := src[argsEnd+1:]
		ret := ""
		if i := strings.IndexByte(rest, '{'); i >= 0 {
			between := strings.TrimSpace(rest[:i])
			if strings.HasPrefix(between, ":") {
				ret = strings.TrimSpace(strings.TrimPrefix(between, ":"))
			}
		}
		if ret == "" {
			if hint := reReturnHint.FindStringSubmatch(decArgs); hint != nil {
				ret = strings.TrimSpace(hint[1])
			}
		}
		name := src[nameStart:nameEnd]
		className := classOfOffset(classes, nameStart)
		qualified := name
		if className != "" {
			qualified = className + "." + name
		}
		op := common.Operation{
			OperationType: toOpKind(decKind),
			Name:          name,
			Arguments:     extractArgNames(argList),
			ReturnType:    ret,
			AnchoredTo:    common.BuildSchemaAnchor(name, path),
			ResolverRef:   common.BuildResolverAnchor(qualified, path),
		}
		ops = append(ops, op)
	}
	return ops
}

// scanBalancedParen advances from src[openIdx] (which must be `(`)
// and returns the index of the matching `)`. ok=false on EOF or
// mismatched parens. Tolerates string and template literals embedded
// inside the argument list.
func scanBalancedParen(src string, openIdx int) (int, bool) {
	if openIdx < 0 || openIdx >= len(src) || src[openIdx] != '(' {
		return -1, false
	}
	depth := 0
	for i := openIdx; i < len(src); i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, true
			}
		case '\'', '"', '`':
			// Skip string body.
			q := src[i]
			i++
			for i < len(src) && src[i] != q {
				if src[i] == '\\' && i+1 < len(src) {
					i += 2
					continue
				}
				i++
			}
		}
	}
	return -1, false
}

func toOpKind(decoratorName string) string {
	switch decoratorName {
	case "Query":
		return "query"
	case "Mutation":
		return "mutation"
	case "Subscription":
		return "subscription"
	}
	return ""
}

type classSpan struct {
	name  string
	start int // byte offset of the class header
	end   int // byte offset just past the class body's closing brace
}

func findClassSpans(src string) []classSpan {
	var out []classSpan
	for _, m := range reClassDecl.FindAllStringSubmatchIndex(src, -1) {
		name := src[m[2]:m[3]]
		// Find the opening `{` after the header.
		brace := strings.IndexByte(src[m[1]:], '{')
		if brace < 0 {
			continue
		}
		bodyStart := m[1] + brace
		end := matchBraceEnd(src, bodyStart)
		if end < 0 {
			continue
		}
		out = append(out, classSpan{name: name, start: m[0], end: end})
	}
	return out
}

func matchBraceEnd(src string, openIdx int) int {
	depth := 0
	for i := openIdx; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func classOfOffset(spans []classSpan, off int) string {
	for _, s := range spans {
		if off >= s.start && off <= s.end {
			return s.name
		}
	}
	return ""
}

func extractArgNames(argList string) []string {
	if strings.TrimSpace(argList) == "" {
		return nil
	}
	var out []string
	for _, m := range reArgName.FindAllStringSubmatch(argList, -1) {
		out = append(out, m[1])
	}
	return out
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
		Frameworks: []string{"type-graphql"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerTx,
		Inputs:     inputs,
		Outputs:    outputs,
	})
}
