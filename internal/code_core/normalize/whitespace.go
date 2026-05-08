package normalize

import (
	"regexp"
	"strings"
)

// canonicalizeWhitespace collapses runs of whitespace into a single space,
// trims leading/trailing whitespace, removes whitespace immediately inside
// brackets, and tightens any whitespace following a comma so that
// `( a int , b string )`, `(a int,b string)`, and `(a int, b string)` all
// collapse to the same canonical form. Brackets covered: `()`, `[]`, `{}`,
// `<>`. The function is pure.
func canonicalizeWhitespace(s string) string {
	s = whitespaceRE.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	s = commaTightenRE.ReplaceAllString(s, ",")
	s = openBracketSpaceRE.ReplaceAllString(s, "$1")
	s = closeBracketSpaceRE.ReplaceAllString(s, "$1")
	return s
}

var (
	whitespaceRE        = regexp.MustCompile(`\s+`)
	openBracketSpaceRE  = regexp.MustCompile(`([\(\[\{<])\s+`)
	closeBracketSpaceRE = regexp.MustCompile(`\s+([\)\]\}>])`)
	// Strip whitespace flanking each comma so that `(a , b)` and
	// `(a,b)` and `(a, b)` all become `(a,b)`. Choosing the no-space
	// form keeps the canonical signature compact and byte-stable
	// regardless of the source-formatter style on either side.
	commaTightenRE = regexp.MustCompile(`\s*,\s*`)
)
