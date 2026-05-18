package jsonrpc

import (
	"fmt"
	"strings"

	"github.com/alecthomas/participle/v2"
	"github.com/alecthomas/participle/v2/lexer"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// Filter is the full Participle grammar for kernel.subscribe filter
// expressions per SPEC §6.16 + plan/answers/06 §W. Replaces the
// pre-F20 hand-rolled `layer` / `layer/kind` shorthand parser.
//
// Supported predicates:
//
//	layer       = "code.core"
//	kind        ~ "FileChanged"   // suffix-prefix match against the event kind
//	produced_by = "extractor:lsp:go"
//
// Predicates compose with AND / OR / NOT and parentheses:
//
//	layer = "code.core" AND kind = "FileChanged"
//	(layer = "code.core" OR layer = "semantic.overlay") AND NOT kind = "Heartbeat"
//
// Shorthand back-compat:
//
//	"code.core"                 → layer = "code.core"
//	"code.core/FileChanged"     → layer = "code.core" AND kind = "FileChanged"
//
// Empty string matches everything (kernel.EventFilter{}).
type Filter struct {
	Or *OrFilter `@@`
}

// OrFilter is a sequence of AndFilters joined by OR.
type OrFilter struct {
	First *AndFilter   `@@`
	Rest  []*AndFilter `( "OR" @@ )*`
}

// AndFilter is a sequence of NotFilters joined by AND.
type AndFilter struct {
	First *NotFilter   `@@`
	Rest  []*NotFilter `( "AND" @@ )*`
}

// NotFilter wraps an Atom, optionally negated.
type NotFilter struct {
	Negate bool  `( @"NOT" )?`
	Atom   *Atom `@@`
}

// Atom is either a parenthesized sub-expression or a Predicate.
type Atom struct {
	Group     *Filter    `  "(" @@ ")"`
	Predicate *Predicate `| @@`
}

// Predicate is one (field op value) triple.
type Predicate struct {
	Field string `@( "layer" | "kind" | "produced_by" )`
	Op    string `@( "=" | "~" )`
	Value string `@String`
}

var filterLexer = lexer.MustSimple([]lexer.SimpleRule{
	{Name: "whitespace", Pattern: `[ \t\r\n]+`},
	{Name: "Ident", Pattern: `[a-zA-Z_][a-zA-Z0-9_]*`},
	{Name: "String", Pattern: `"(\\"|[^"])*"`},
	{Name: "Op", Pattern: `=|~`},
	{Name: "Punct", Pattern: `[\(\)]`},
})

var filterParser = participle.MustBuild[Filter](
	participle.Lexer(filterLexer),
	participle.Unquote("String"),
	participle.UseLookahead(2),
)

// ParseFilterExpr parses expr into a Filter AST. Returns nil + nil for
// the empty string (match-all). Used by parseFilter to back-translate
// into a kernel.EventFilter; exposed for tests.
func ParseFilterExpr(expr string) (*Filter, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, nil
	}
	// Back-compat shorthand: bare ident or ident/ident lowers to
	// `layer = "X"` (and optionally `AND kind = "Y"`). Detect by the
	// absence of grammar markers (= ~ "( OR AND NOT).
	if isShorthand(expr) {
		return shorthandToFilter(expr), nil
	}
	return filterParser.ParseString("filter", expr)
}

// isShorthand detects the legacy layer / layer/kind form. We treat
// any expression that contains no `"`, `=`, `~`, `(`, `OR`, `AND`,
// `NOT` (case-sensitive — the grammar reserves them as keywords) as
// shorthand. False positives are harmless: the shorthand path
// produces the same AST the grammar would for `layer = "X"`.
func isShorthand(expr string) bool {
	if strings.ContainsAny(expr, `"=~()`) {
		return false
	}
	for _, kw := range []string{" OR ", " AND ", " NOT ", "OR ", "AND ", "NOT "} {
		if strings.Contains(expr, kw) {
			return false
		}
	}
	return true
}

func shorthandToFilter(expr string) *Filter {
	layer, kind, hasKind := strings.Cut(expr, "/")
	layerPred := &Predicate{Field: "layer", Op: "=", Value: strings.TrimSpace(layer)}
	first := &NotFilter{Atom: &Atom{Predicate: layerPred}}
	and := &AndFilter{First: first}
	if hasKind {
		kindPred := &Predicate{Field: "kind", Op: "=", Value: strings.TrimSpace(kind)}
		and.Rest = []*NotFilter{{Atom: &Atom{Predicate: kindPred}}}
	}
	return &Filter{Or: &OrFilter{First: and}}
}

// ToEventFilter lowers the parsed Filter AST into a kernel.EventFilter
// the event log understands. The grammar is richer than today's
// EventFilter struct (which only carries Layers / Kinds lists), so we
// extract layer/kind predicates that are joined under a single AND
// chain and ignore everything else with a clear error.
//
// Future: extend kernel.EventFilter with a typed predicate tree so we
// can represent OR / NOT / produced_by faithfully. Today this is the
// minimum that ships per F20's "lowers to kernel.EventFilter, keeps
// shorthand sugar" gate.
func (f *Filter) ToEventFilter() (kernel.EventFilter, error) {
	out := kernel.EventFilter{}
	if f == nil || f.Or == nil {
		return out, nil
	}
	// Reject OR at the top level for the v1 lowering. The grammar
	// accepts OR for forward compatibility, but the EventFilter shape
	// cannot represent disjunction yet; surface a clear error.
	if len(f.Or.Rest) > 0 {
		return out, fmt.Errorf("filter: OR not supported in v1 EventFilter lowering (parsed but not executable)")
	}
	andClause := f.Or.First
	if andClause == nil {
		return out, nil
	}
	if err := mergeNotFilter(andClause.First, &out); err != nil {
		return out, err
	}
	for _, n := range andClause.Rest {
		if err := mergeNotFilter(n, &out); err != nil {
			return out, err
		}
	}
	return out, nil
}

func mergeNotFilter(n *NotFilter, out *kernel.EventFilter) error {
	if n == nil {
		return nil
	}
	if n.Negate {
		return fmt.Errorf("filter: NOT not supported in v1 EventFilter lowering")
	}
	if n.Atom == nil {
		return nil
	}
	if n.Atom.Group != nil {
		nested, err := n.Atom.Group.ToEventFilter()
		if err != nil {
			return err
		}
		out.Layers = append(out.Layers, nested.Layers...)
		out.Kinds = append(out.Kinds, nested.Kinds...)
		return nil
	}
	if n.Atom.Predicate == nil {
		return nil
	}
	p := n.Atom.Predicate
	if p.Op != "=" {
		return fmt.Errorf("filter: only = supported in v1 (got %s)", p.Op)
	}
	switch p.Field {
	case "layer":
		out.Layers = append(out.Layers, p.Value)
	case "kind":
		out.Kinds = append(out.Kinds, p.Value)
	case "produced_by":
		return fmt.Errorf("filter: produced_by not supported in v1 EventFilter lowering (parsed but not executable)")
	default:
		return fmt.Errorf("filter: unknown predicate field %q", p.Field)
	}
	return nil
}
