// Package strawberry implements the graphql.py.strawberry extractor.
// Strawberry is a Python GraphQL framework that uses dataclass-style
// type classes (`@strawberry.type`) plus decorated resolver methods:
//   - `@strawberry.field` on a Query/Mutation class method declares a
//     field resolver.
//   - `@strawberry.mutation` is the conventional alias for mutations.
//   - `@strawberry.subscription` is the alias for subscriptions.
//
// We classify the parent class via its name (`Query` / `Mutation` /
// `Subscription`) when the method decorator is the generic
// `@strawberry.field`. Strawberry users often (but not always) follow
// this convention.
package strawberry

import (
	"context"
	"regexp"
	"strings"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"

	"github.com/shivamstaq/graph-harness/extractors/graphql/common"
)

const extractorName = "graphql.py.strawberry"

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
		Frameworks: []string{"strawberry"},
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
	if !strings.Contains(text, "strawberry") {
		return nil, nil
	}
	ops := Extract(path, text)
	return common.EmitOperations(ops)
}

// reClassDecl captures the enclosing class name plus its decorator
// list. We rely on `strawberry.type` (or `strawberry.input` / `.interface`)
// being a class-level decorator on the immediately-preceding lines.
var reClassDecl = regexp.MustCompile(`(?m)^class\s+([A-Za-z_][A-Za-z0-9_]*)\s*[:\(]`)

// reMethodDef captures one method definition inside the class body,
// including its decorator list (up to 3 lines of decorators).
var reMethodDef = regexp.MustCompile(`(?m)^[\t ]+(?:async\s+)?def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(([^)]*)\)\s*(?:->\s*([^\:]+?))?\s*:`)

// reArgName extracts argument names from a Python def signature.
// Skips `self`/`cls` plus root/info args (`root`, `info`).
var reArgName = regexp.MustCompile(`(?:^|,)\s*([A-Za-z_][A-Za-z0-9_]*)\s*[:=,)]`)

// Extract is the pure scanning routine exposed for tests.
func Extract(path, src string) []common.Operation {
	var ops []common.Operation
	classes := findClassSpans(src)
	lines := strings.Split(src, "\n")
	// Build a line→byte-offset table so we can attribute each match
	// to a class span.
	offsets := make([]int, len(lines))
	pos := 0
	for i, l := range lines {
		offsets[i] = pos
		pos += len(l) + 1
	}
	for _, m := range reMethodDef.FindAllStringSubmatchIndex(src, -1) {
		methodStart := m[0]
		name := src[m[2]:m[3]]
		args := src[m[4]:m[5]]
		ret := ""
		if m[6] >= 0 {
			ret = strings.TrimSpace(src[m[6]:m[7]])
		}
		// Look at the up-to-3 lines preceding the def for a
		// strawberry decorator.
		decorator := lookbackStrawberryDecorator(src, methodStart)
		if decorator == "" {
			continue
		}
		opKind := classifyDecorator(decorator)
		className := classOfOffset(classes, methodStart)
		if opKind == "" {
			opKind = inferFromClassName(className)
		}
		if opKind == "" {
			continue
		}
		qualified := name
		if className != "" {
			qualified = className + "." + name
		}
		ops = append(ops, common.Operation{
			OperationType: opKind,
			Name:          name,
			Arguments:     extractArgNames(args),
			ReturnType:    ret,
			AnchoredTo:    common.BuildSchemaAnchor(name, path),
			ResolverRef:   common.BuildResolverAnchor(qualified, path),
		})
	}
	return ops
}

// lookbackStrawberryDecorator scans up to 3 non-empty lines before
// methodStart for a `@strawberry.<thing>` decorator. Returns the
// matched decorator string (without the leading @).
func lookbackStrawberryDecorator(src string, methodStart int) string {
	// Read lines backward.
	end := methodStart
	for tries := 0; tries < 3; tries++ {
		// Find the start of the line that ends at `end-1`.
		lineEnd := end - 1
		if lineEnd < 0 {
			return ""
		}
		lineStart := lineEnd
		for lineStart > 0 && src[lineStart-1] != '\n' {
			lineStart--
		}
		line := strings.TrimSpace(src[lineStart:lineEnd])
		if line == "" {
			end = lineStart
			continue
		}
		if strings.HasPrefix(line, "@") {
			tok := strings.TrimPrefix(line, "@")
			// Strip parens if present.
			if i := strings.IndexByte(tok, '('); i > 0 {
				tok = tok[:i]
			}
			if strings.HasPrefix(tok, "strawberry.") {
				return tok
			}
		}
		// Non-decorator non-empty line — stop scanning back.
		return ""
	}
	return ""
}

// classifyDecorator maps a `strawberry.<x>` decorator to an
// operation kind, or empty when the decorator does not by itself
// identify the kind (generic `.field`).
func classifyDecorator(dec string) string {
	switch dec {
	case "strawberry.field":
		return "" // need parent-class hint
	case "strawberry.mutation":
		return "mutation"
	case "strawberry.subscription":
		return "subscription"
	}
	return ""
}

func inferFromClassName(name string) string {
	switch name {
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
	name       string
	headerByte int
	indentLen  int
	endByte    int
}

// findClassSpans scans for `class Name:` blocks; the span ends when
// the indentation falls back to the class-header level (or EOF). We
// keep this simple because Python disallows non-indented continuation
// inside a class body.
func findClassSpans(src string) []classSpan {
	var out []classSpan
	for _, m := range reClassDecl.FindAllStringSubmatchIndex(src, -1) {
		name := src[m[2]:m[3]]
		header := m[0]
		// Locate end-of-line for the header.
		eol := strings.IndexByte(src[header:], '\n')
		if eol < 0 {
			out = append(out, classSpan{name: name, headerByte: header, endByte: len(src)})
			continue
		}
		bodyStart := header + eol + 1
		end := classBodyEnd(src, bodyStart)
		out = append(out, classSpan{name: name, headerByte: header, endByte: end})
	}
	return out
}

// classBodyEnd advances from bodyStart until a line at column 0 (no
// indentation) is encountered. Returns that line's start offset.
func classBodyEnd(src string, bodyStart int) int {
	i := bodyStart
	for i < len(src) {
		// Skip blank / comment lines.
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
		// Look for non-whitespace at column 0 → end of class body.
		if !(line[0] == ' ' || line[0] == '\t') {
			return lineStart
		}
		i += eol + 1
	}
	return len(src)
}

func classOfOffset(spans []classSpan, off int) string {
	for _, s := range spans {
		if off >= s.headerByte && off <= s.endByte {
			return s.name
		}
	}
	return ""
}

func extractArgNames(argList string) []string {
	if strings.TrimSpace(argList) == "" {
		return nil
	}
	skip := map[string]bool{"self": true, "cls": true, "root": true, "info": true}
	// Append a trailing ',' so the regex sees an arg-trailing delim.
	src := argList + ","
	var out []string
	seen := map[string]struct{}{}
	for _, m := range reArgName.FindAllStringSubmatch(src, -1) {
		name := m[1]
		if skip[name] {
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

func init() {
	cf.Register(extractorName, New, cf.Descriptor{
		Family:     "graphql",
		Languages:  []string{"python"},
		Frameworks: []string{"strawberry"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerTx,
		Inputs:     inputs,
		Outputs:    outputs,
	})
}
