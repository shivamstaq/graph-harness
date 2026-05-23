// Package prisma implements the Prisma schema extractor. It parses
// `schema.prisma` files: one Schema entity per `model` block, one
// SchemaField per non-relation field within the block.
//
// Anchor strategy: Schema entities anchor on `qualified_name = <model>`
// (model names are workspace-unique by Prisma convention). SchemaField
// entities anchor on `qualified_name = <model>.<field>` per the
// Pass-0.5-A convention encoded in
// internal/semantic_overlay/anchors/schema.go.
//
// Relation fields (e.g. `posts Post[]`) are skipped — they're handled
// by the relations Pass in the semantic overlay, not by code.framework.
package prisma

import (
	"context"
	"regexp"
	"strings"

	"github.com/shivamstaq/graph-harness/extractors/schema/common"
	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Name is the registry identifier for this extractor.
const Name = "schema.prisma"

// extractor is the per-instance state. Stateless across calls but
// holds the workspace path + logger from Deps.
type extractor struct {
	workspace string
	logf      func(string, ...any)
}

// New returns a constructed extractor. Wired into the registry from
// init() below; exported so tests can build instances without going
// through Lookup.
func New(deps cf.Deps) (cf.Extractor, error) {
	return &extractor{workspace: deps.Workspace, logf: deps.Logf}, nil
}

func (e *extractor) Name() string                  { return Name }
func (e *extractor) Inputs() []cf.EventKind        { return []cf.EventKind{cf.InputCoreFileChanged} }
func (e *extractor) Outputs() []cf.EntityKind {
	return []cf.EntityKind{cf.KindSchema, cf.KindSchemaField}
}
func (e *extractor) Capabilities() cf.Capabilities {
	return cf.Capabilities{
		Family:     "schemas",
		Languages:  []string{"typescript", "python", "go"}, // any project
		Frameworks: []string{"prisma"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
	}
}

// accept returns true for the Prisma schema DSL file.
func accept(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".prisma")
}

// OnEvent reads the Prisma schema file mentioned in the event,
// parses model blocks, and emits one Schema + one SchemaField per
// detected (model, field). The DSL is small enough that a regex
// recogniser is the simplest correct shape; we don't pull in a full
// Prisma grammar.
func (e *extractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	_ = ctx
	path, ok := common.NoopOnPathMiss(ctx, in, accept)
	if !ok {
		return nil, nil
	}
	src, err := common.ReadFile(e.workspace, path)
	if err != nil {
		return nil, nil // file gone / unreadable — suppress-at-source
	}
	return Parse(path, src), nil
}

// Parse is exposed for tests: given a Prisma schema's source bytes,
// return the events that would be emitted. Test fixtures call this
// directly to avoid spinning up an EventLog.
func Parse(path string, src []byte) []kernel.Event {
	models := splitModels(string(src))
	if len(models) == 0 {
		return nil
	}
	producedBy := common.ProducedBy(Name)
	var out []kernel.Event
	for _, m := range models {
		schemaAnchor := common.QualifiedAnchor(m.name, path)
		schema, schemaEv := common.EmitSchema(producedBy, m.name, "prisma", schemaAnchor)
		out = append(out, schemaEv)
		for _, f := range m.fields {
			fieldAnchor := common.QualifiedAnchor(m.name+"."+f.name, path)
			_, fieldEv := common.EmitField(producedBy, schema, f.name, f.dataType, f.nullable, fieldAnchor)
			out = append(out, fieldEv)
		}
	}
	return out
}

// --- Parser internals ------------------------------------------------------

type modelBlock struct {
	name   string
	fields []fieldDecl
}

type fieldDecl struct {
	name     string
	dataType string
	nullable bool
}

// modelHeaderRE matches `model Foo {` — captures the model name.
var modelHeaderRE = regexp.MustCompile(`(?m)^\s*model\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{`)

// fieldRE matches `name TypeRef[?]` lines inside a model block. We
// stop at the first whitespace+attribute (e.g. `@id`, `@default(...)`)
// to keep the parser simple. Relation fields (`posts Post[]`) are
// filtered out by the caller because the type ends with `]`.
var fieldRE = regexp.MustCompile(`(?m)^\s*([a-zA-Z_][a-zA-Z0-9_]*)\s+([A-Za-z0-9_]+)(\?)?(\s|$|@)`)

// splitModels finds model blocks at the top level of the source and
// returns each one's name + parsed fields. Brace-counting is
// deliberately simple: nested `{` inside attributes is rare in Prisma
// and is tolerated by counting depth.
func splitModels(src string) []modelBlock {
	// Strip line comments to avoid matching `model` inside them.
	clean := stripLineComments(src)
	heads := modelHeaderRE.FindAllStringSubmatchIndex(clean, -1)
	var out []modelBlock
	for _, h := range heads {
		name := clean[h[2]:h[3]]
		// Find the matching close-brace by scanning depth from h[1]-1.
		depth := 1
		end := -1
		for i := h[1]; i < len(clean); i++ {
			switch clean[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			continue
		}
		body := clean[h[1]:end]
		out = append(out, modelBlock{name: name, fields: parseFields(body)})
	}
	return out
}

// parseFields walks the body of a model block and returns the
// non-relation field declarations. Relation fields are recognised by
// either a type ending in `[]` or a capitalised type name that has a
// `@relation(...)` attribute — for the latter we err on the side of
// keeping the field (the relation attribute is rare without `[]`).
func parseFields(body string) []fieldDecl {
	var out []fieldDecl
	for _, line := range strings.Split(body, "\n") {
		line = stripInlineComment(line)
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Skip block markers like @@id([...]), @@unique([...]), @@map.
		if strings.HasPrefix(trimmed, "@@") {
			continue
		}
		m := fieldRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		dataType := m[2]
		nullable := m[3] == "?"
		// Relation arrays: scan for "[]" right after the type. fieldRE
		// stops at whitespace/@, so we re-check the raw line.
		afterType := strings.TrimPrefix(strings.TrimSpace(line), name)
		afterType = strings.TrimSpace(afterType)
		if strings.HasPrefix(afterType, dataType+"[]") {
			// list / relation; skip
			continue
		}
		out = append(out, fieldDecl{name: name, dataType: dataType, nullable: nullable})
	}
	return out
}

// stripLineComments removes `//` comments. Block comments are not used
// in Prisma; we don't handle them.
func stripLineComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if idx := strings.Index(line, "//"); idx >= 0 {
			b.WriteString(line[:idx])
		} else {
			b.WriteString(line)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// stripInlineComment trims a trailing `//...` from a single line.
func stripInlineComment(line string) string {
	if idx := strings.Index(line, "//"); idx >= 0 {
		return line[:idx]
	}
	return line
}

func init() {
	cf.Register(Name, New, cf.Descriptor{
		Family:     "schemas",
		Languages:  []string{"typescript", "python", "go"},
		Frameworks: []string{"prisma"},
		Fallback:   cf.FallbackTreesitterOnly,
		BatchHint:  cf.BatchPerEvent,
		Inputs:     []cf.EventKind{cf.InputCoreFileChanged},
		Outputs:    []cf.EntityKind{cf.KindSchema, cf.KindSchemaField},
	})
}
