// Package dsl is the single Participle v2 parser for the .gh surface form.
//
// SPEC §11.2 surface scope: selector, flow, query, import declarations.
// Selectors carry a multi-anchor ladder (qualified_name, function_signature,
// body_hash, call_neighborhood, symbol_fingerprint, ast_hash, path_glob) plus
// optional thresholds. The fallback keyword is a synonym for anchor that
// signals lower priority to a human reader; both are equally valid entries on
// the resolution ladder. Datalog `rule … :- …` is rejected with a clear error
// message until P3 lands the rule engine.
package dsl

import (
	"fmt"
	"io"
	"strings"

	"github.com/alecthomas/participle/v2"
	"github.com/alecthomas/participle/v2/lexer"
)

// File is a parsed .gh source file. Authors mix declarations freely.
type File struct {
	Decls []*Decl `@@*`
}

// Decl is one top-level declaration. Exactly one branch is non-nil.
type Decl struct {
	Import   *Import   `  @@`
	Selector *Selector `| @@`
	Flow     *Flow     `| @@`
	Query    *Query    `| @@`
	Rule     *Rule     `| @@`
}

// Import lifts another .gh file into the current scope, namespaced.
//
//	import "selectors/payments.gh" as p
type Import struct {
	Path  string `"import" @String`
	Alias string `"as" @Ident`
}

// Selector declares a named multi-anchor matcher.
//
// SPEC §3.3: anchors are an ordered ladder; the resolver evaluates top-down
// and the first anchor above its threshold wins. The optional `thresholds`
// block overrides per-outcome defaults.
type Selector struct {
	Name       string      `"selector" @Ident "{"`
	Unique     bool        `( @"unique"`
	Anchors    []*Anchor   `| @@`
	Thresholds *Thresholds `| @@ )*`
	End        struct{}    `"}"`
}

// Anchor is one entry on the multi-anchor ladder. Marker is the literal
// keyword used in source (`anchor` or `fallback`); both forms are preserved
// to keep round-trip rendering byte-stable.
type Anchor struct {
	Marker string       `@("anchor" | "fallback")`
	Kind   string       `@Ident`
	Value  *AnchorValue `( @@ )?`
}

// AnchorValue is the right-hand side of an anchor declaration. Exactly one
// of the fields is non-nil after a successful parse. The shape is keyed
// implicitly by the surrounding anchor kind:
//
//	qualified_name / body_hash / symbol_fingerprint / ast_hash / path_glob → Str
//	function_signature                                                     → Sig
//	call_neighborhood                                                      → Neighbor
type AnchorValue struct {
	Str      *string           `  @String`
	Int      *int              `| @Int`
	Float    *float64          `| @Float`
	Sig      *FunctionSig      `| @@`
	Neighbor *CallNeighborhood `| @@`
}

// FunctionSig is the body of `anchor function_signature sig(<types> -> <ret>)`.
// Param/return types are arbitrary identifiers (dotted names allowed); the
// per-language signature normalizer in code_core/normalize/ produces the
// canonical strings these are matched against.
type FunctionSig struct {
	LParen struct{}   `"sig" "("`
	Params []string   `( @Ident ( "," @Ident )* )?`
	RParen struct{}   `")"`
	Arrow  struct{}   `"->"`
	Return *SigReturn `@@`
}

// SigReturn is either a single named return type or a parenthesized tuple.
type SigReturn struct {
	Single *string   `  @Ident`
	Tuple  *SigTuple `| @@`
}

// SigTuple is `(T1, T2, …)` on the right of the arrow.
type SigTuple struct {
	LParen struct{} `"("`
	Items  []string `( @Ident ( "," @Ident )* )?`
	RParen struct{} `")"`
}

// CallNeighborhood is the body of
//
//	anchor call_neighborhood {
//	  callers: [...]
//	  callees: [...]
//	}
//
// Either field may be omitted. Callers' / callees' values are qualified-name
// strings matched against the code_core adjacency table.
type CallNeighborhood struct {
	LBrace  struct{}    `"{"`
	Callers *StringList `( "callers" ":" @@ )?`
	Callees *StringList `( "callees" ":" @@ )?`
	RBrace  struct{}    `"}"`
}

// StringList is `[ "a", "b", … ]`.
type StringList struct {
	LBrack struct{} `"["`
	Items  []string `( @String ( "," @String )* )?`
	RBrack struct{} `"]"`
}

// Thresholds is the optional `thresholds { … }` block.
//
// `bound` and `reanchored` are single floats; `ambiguous_zone` is a two-element
// `[lo, hi]` range. Parser accepts the full SPEC §3.3 surface; the resolver
// honors `bound` and `reanchored` in P1, while `ambiguous_zone` is parsed but
// only enforced once the `ambiguous` outcome lands in P3.
type Thresholds struct {
	Begin struct{}         `"thresholds" "{"`
	Items []*ThresholdItem `( @@ )*`
	End   struct{}         `"}"`
}

// ThresholdItem is `<name>: <number>` or `<name>: [lo, hi]`.
type ThresholdItem struct {
	Name  string      `@Ident ":"`
	Float *float64    `(  @Float`
	Int   *int        ` | @Int`
	Range *FloatRange ` | @@ )`
}

// FloatRange is a two-element `[lo, hi]` literal.
type FloatRange struct {
	LBrack struct{} `"["`
	Lo     float64  `@Float ","`
	Hi     float64  `@Float`
	RBrack struct{} `"]"`
}

// Lit is a string, integer, or float literal (used by Query bodies).
type Lit struct {
	Str   *string  `  @String`
	Int   *int     `| @Int`
	Float *float64 `| @Float`
}

// Flow declares a multi-step semantic flow.
type Flow struct {
	Name        string      `"flow" @Ident "{"`
	Description string      `( "description" @String )?`
	Scope       string      `( "scope" @Ident )?`
	Risk        string      `( "risk" @Ident )?`
	Steps       []*FlowStep `( @@ )*`
	End         struct{}    `"}"`
}

// FlowStep is one named step in a flow.
type FlowStep struct {
	Name    string  `"step" @Ident`
	Targets *Target `"targets" @@`
}

// Target is the resolution target for a step. v0 supports inline-selector form.
type Target struct {
	InlineSelector *InlineSelector `"selector" @@`
}

// InlineSelector is a one-off selector with anchors literal-encoded.
//
// Anchors inside an inline selector use the compact `<kind> <value>` form
// (no `anchor` keyword). Multi-anchor inline selectors separate entries with
// optional `;` for readability.
type InlineSelector struct {
	LBrace  struct{}        `"{"`
	Anchors []*InlineAnchor `( @@ ( ";"? @@ )* )?`
	RBrace  struct{}        `"}"`
}

// InlineAnchor is a `<kind> <value>` inside an inline selector body.
type InlineAnchor struct {
	Kind  string       `@Ident`
	Value *AnchorValue `@@`
}

// Query is a named DSL query. v0 supports Cypher match/return only.
type Query struct {
	Name string   `"query" @String "{"`
	Body string   `@( ~"}" )*`
	End  struct{} `"}"`
}

// Rule is the Datalog form. P0/P1 reject these in semantic validation; the
// grammar accepts them so we can produce a clear "deferred to P3" error.
type Rule struct {
	Head string `"rule" @Ident`
	Args string `"(" @( ~")" )* ")"`
	Body string `":-" @( ~"." )* "."`
}

var ghLexer = lexer.MustSimple([]lexer.SimpleRule{
	{Name: "Comment", Pattern: `(?:\/\/[^\n]*|\/\*[\s\S]*?\*\/)`},
	{Name: "Arrow", Pattern: `\->`},
	{Name: "Float", Pattern: `[-+]?\d+\.\d+`},
	{Name: "Int", Pattern: `[-+]?\d+`},
	{Name: "Ident", Pattern: `[a-zA-Z_][a-zA-Z0-9_\.]*`},
	{Name: "String", Pattern: `"(\\"|[^"])*"`},
	{Name: "Punct", Pattern: `[\{\}\(\)\[\],:=;]`},
	{Name: "whitespace", Pattern: `[ \t\r\n]+`},
})

var fileParser = participle.MustBuild[File](
	participle.Lexer(ghLexer),
	participle.Elide("Comment"),
	participle.Unquote("String"),
	participle.UseLookahead(2),
)

// Parse parses the .gh source from r into a File AST.
func Parse(r io.Reader, name string) (*File, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return ParseString(name, string(data))
}

// ParseString parses .gh source from a string.
func ParseString(name, source string) (*File, error) {
	// Pre-flight: Datalog rules are deferred to P3 per plan §P0.T15. The
	// lexer does not handle `:-` so a Datalog body produces a confusing
	// "invalid input" error. Detect the pattern first and surface the
	// project-specific message.
	if hasDatalogRule(source) {
		return nil, fmt.Errorf("datalog rules deferred to P3; v0 supports selector/flow/query/import only")
	}
	f, err := fileParser.ParseString(name, source)
	if err != nil {
		return nil, err
	}
	if err := validateSurface(f); err != nil {
		return nil, err
	}
	return f, nil
}

// hasDatalogRule scans source for the `rule <ident>(...) :- ...` pattern.
// Conservative: any `:-` outside a string literal triggers rejection.
func hasDatalogRule(src string) bool {
	in := src
	// Strip string contents to avoid false positives.
	for {
		i := strings.IndexByte(in, '"')
		if i < 0 {
			break
		}
		j := strings.IndexByte(in[i+1:], '"')
		if j < 0 {
			break
		}
		in = in[:i] + in[i+j+2:]
	}
	return strings.Contains(in, ":-")
}

// validateSurface enforces SPEC §11.2: Datalog rules are deferred to P3 with
// a clear message. Callers see a single, helpful error rather than discovering
// it during evaluation later.
func validateSurface(f *File) error {
	for _, d := range f.Decls {
		if d.Rule != nil {
			return fmt.Errorf("datalog rules deferred to P3 (rule %s); v0 supports selector/flow/query/import only", d.Rule.Head)
		}
	}
	return nil
}
