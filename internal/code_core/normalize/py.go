package normalize

import "strings"

// Python canonicalizes a Python function signature for code.core
// identity.
//
// Concretely it:
//
//   - collapses whitespace and tightens commas as [Go] does;
//   - rewrites a leading PEP 695 type-parameter block of the form
//     `[T, U: Hashable](...)` so the parameter names become `T0, T1,
//     ...` in declaration order, with whole-word substitution into the
//     rest of the signature;
//   - sorts the keyword-only block (everything after a bare `*` or
//     `*args` separator, up to a trailing `**kwargs` or the closing
//     paren) by parameter name. Python's call ABI treats those entries
//     as unordered, so two source-text orderings of the same kw-only
//     set must yield the same canonical signature. Positional and
//     positional-or-keyword parameters before `*` keep their order
//     because they are ordered in the call ABI.
//
// The signature input is expected to be the parenthesized parameter
// list optionally followed by `-> ReturnType` (the shape tree-sitter's
// Python grammar exposes). Inputs without an opening `(` are returned
// unchanged after whitespace canonicalization.
func Python(sig string) string {
	if sig == "" {
		return sig
	}
	out := canonicalizeWhitespace(sig)
	out = canonicalizeGenerics(out, '[', ']')
	out = sortPythonKeywordOnly(out)
	return out
}

// sortPythonKeywordOnly locates the parameter list `(...)` at the start
// of sig and, if it contains a `*` / `*args` keyword-only separator,
// sorts every entry between the separator and the next `**kwargs`
// entry (or the closing paren) by leading parameter name.
//
// Entries are split on top-level commas (commas inside any nested
// bracket pair are ignored), preserving annotations and defaults
// verbatim. A leading `*` or `**` prefix on an entry name is treated
// as part of the kind marker rather than the name.
func sortPythonKeywordOnly(sig string) string {
	openIdx := strings.IndexByte(sig, '(')
	if openIdx < 0 {
		return sig
	}
	closeIdx := matchingParen(sig, openIdx)
	if closeIdx < 0 {
		return sig
	}
	inner := sig[openIdx+1 : closeIdx]
	parts := splitTopLevelCommas(inner)
	if len(parts) == 0 {
		return sig
	}

	// Walk parts to find the kw-only boundary. The boundary is the
	// first entry whose leading non-whitespace text is `*` or
	// `*<identifier>` (i.e. `*args`). Everything strictly after that
	// entry, up to (but not including) a `**kwargs`-style entry, is
	// the unordered region.
	boundary := -1
	tail := len(parts) // index of the first entry not in the kw-only region
	for i, p := range parts {
		t := strings.TrimSpace(p)
		if boundary < 0 && isKeywordOnlySeparator(t) {
			boundary = i
			continue
		}
		if boundary >= 0 && strings.HasPrefix(t, "**") {
			tail = i
			break
		}
	}
	if boundary < 0 || boundary+1 >= tail {
		return sig
	}

	region := parts[boundary+1 : tail]
	sorted := make([]string, len(region))
	copy(sorted, region)
	sortByLeadingName(sorted)

	out := make([]string, 0, len(parts))
	out = append(out, parts[:boundary+1]...)
	out = append(out, sorted...)
	out = append(out, parts[tail:]...)

	var b strings.Builder
	b.Grow(len(sig))
	b.WriteString(sig[:openIdx+1])
	b.WriteString(strings.Join(out, ","))
	b.WriteString(sig[closeIdx:])
	return b.String()
}

// isKeywordOnlySeparator reports whether s is a bare `*` or a
// `*<identifier>` *args entry — both of which terminate the
// positional region in a Python parameter list.
func isKeywordOnlySeparator(s string) bool {
	if s == "*" {
		return true
	}
	if !strings.HasPrefix(s, "*") || strings.HasPrefix(s, "**") {
		return false
	}
	// `*args`, `*args: list[int]`, `*args = ()`, ...
	rest := s[1:]
	if rest == "" {
		return false
	}
	return isIdentStart(rest[0])
}

// matchingParen returns the index of the `)` that closes the `(` at
// index open in s, respecting nesting. -1 if unmatched.
func matchingParen(s string, open int) int {
	if open < 0 || open >= len(s) || s[open] != '(' {
		return -1
	}
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// sortByLeadingName sorts entries by their leading parameter name.
// The leading name is taken to be the first identifier (after
// trimming whitespace and any `*` / `**` prefix). Entries without an
// extractable name sort to the end in input order.
func sortByLeadingName(entries []string) {
	keys := make([]string, len(entries))
	for i, e := range entries {
		keys[i] = pythonParamKey(e)
	}
	// Insertion sort — the keyword-only region is small (< 16 typical),
	// and a stable sort is convenient without dragging in sort.Slice
	// here.
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && lessParamKey(keys[j], keys[j-1]); j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}

func pythonParamKey(s string) string {
	t := strings.TrimSpace(s)
	for strings.HasPrefix(t, "*") {
		t = t[1:]
	}
	t = strings.TrimSpace(t)
	return leadingIdentifier(t)
}

// lessParamKey orders empty keys after non-empty keys; otherwise
// lexicographic.
func lessParamKey(a, b string) bool {
	switch {
	case a == "" && b == "":
		return false
	case a == "":
		return false
	case b == "":
		return true
	default:
		return a < b
	}
}
