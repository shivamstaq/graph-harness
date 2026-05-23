package common

import (
	"regexp"
	"strings"
)

// SDLField is one operation field declared inside a
// `type Query { ... }` / `type Mutation { ... }` /
// `type Subscription { ... }` block of GraphQL SDL.
type SDLField struct {
	OperationType string   // "query" | "mutation" | "subscription"
	Name          string
	Arguments     []string
	ReturnType    string
}

// reTypeBlock matches `type Query { ... }`, including `extend type`,
// across the closing brace. We do not aim to handle every legal SDL
// (escape sequences, comments) — best-effort per SPEC §6.11.
var reTypeBlock = regexp.MustCompile(`(?s)\b(?:extend\s+)?type\s+(Query|Mutation|Subscription)\s*\{([^}]*)\}`)

// reField matches `fieldName(arg1: Int, arg2: String!): ReturnType`
// or the no-arg form `fieldName: ReturnType`. Names are simple
// identifiers; arguments and return type are captured raw.
var reField = regexp.MustCompile(`(?m)^[\t ]*([A-Za-z_][A-Za-z0-9_]*)\s*(?:\(([^)]*)\))?\s*:\s*([^\r\n#]+?)\s*(?:#.*)?$`)

// reArg matches one argument decl `name: Type` in the parenthesized
// argument list. Default values are tolerated and skipped.
var reArg = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*:`)

// ParseSDL scans src for `type Query/Mutation/Subscription { ... }`
// blocks and returns one SDLField per declared field. The src may be
// the contents of a `gql` tagged template literal, a `.graphql` file,
// or an inline string literal; we do not validate that the surrounding
// language is GraphQL.
func ParseSDL(src string) []SDLField {
	var out []SDLField
	for _, m := range reTypeBlock.FindAllStringSubmatch(src, -1) {
		op := toOpKind(m[1])
		body := m[2]
		for _, fm := range reField.FindAllStringSubmatch(body, -1) {
			name := fm[1]
			if name == "" {
				continue
			}
			args := parseArgs(fm[2])
			ret := strings.TrimSpace(fm[3])
			out = append(out, SDLField{
				OperationType: op,
				Name:          name,
				Arguments:     args,
				ReturnType:    ret,
			})
		}
	}
	return out
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

func parseArgs(argList string) []string {
	if argList == "" {
		return nil
	}
	var out []string
	for _, m := range reArg.FindAllStringSubmatch(argList, -1) {
		out = append(out, m[1])
	}
	return out
}

// reGqlTemplate locates `gql\`...\`` template literal contents (and
// variants like `graphql\`...\``). We scan the whole file for these
// rather than walking tree-sitter — string boundaries inside a
// template literal are tricky to enumerate via the AST and the
// regex form is robust enough for the patterns we target.
var reGqlTemplate = regexp.MustCompile("(?s)(?:^|[^A-Za-z0-9_$])(?:gql|graphql)\\s*`([^`]*)`")

// ExtractGqlBlocks returns the contents of every gql`...` (or
// graphql`...`) template literal in src.
func ExtractGqlBlocks(src string) []string {
	var out []string
	for _, m := range reGqlTemplate.FindAllStringSubmatch(src, -1) {
		out = append(out, m[1])
	}
	return out
}
