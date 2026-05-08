package normalize

import "strings"

// canonicalizeGenerics rewrites the leading type-parameter block of sig (if
// any) to use canonical names T0, T1, ... in declaration order, then
// substitutes those canonical names back into the rest of the signature as
// whole-word identifiers.
//
//	open / close — bracket pair that delimits the type-parameter list
//	("[", "]" for Go and Python PEP 695; "<", ">" for TypeScript).
//
// The leading bracketed block is detected only if sig begins with `open`
// (after any leading whitespace), and the block must close before any
// `(` or `:` (which mark the parameter list). When no such block is
// present the function returns sig unchanged.
//
// Identifier extraction uses a conservative rule: for each comma-separated
// entry inside the block, the first run of `[A-Za-z_][A-Za-z_0-9]*`
// characters is treated as the parameter name. Constraints, defaults, and
// variance markers (`extends`, `=`, `out`, `in`, `*`) are preserved
// verbatim — only the leading identifier is rewritten.
func canonicalizeGenerics(sig string, openBr, closeBr byte) string {
	if sig == "" {
		return sig
	}
	i := 0
	for i < len(sig) && (sig[i] == ' ' || sig[i] == '\t') {
		i++
	}
	if i >= len(sig) || sig[i] != openBr {
		return sig
	}
	// Find the matching close bracket, respecting nested brackets of the
	// same kind. We do not try to be smart about quotes — type-param
	// blocks do not contain string literals in the languages we target.
	depth := 0
	end := -1
	for j := i; j < len(sig); j++ {
		switch sig[j] {
		case openBr:
			depth++
		case closeBr:
			depth--
		}
		if depth == 0 {
			end = j
			break
		}
	}
	if end < 0 {
		return sig
	}
	inner := sig[i+1 : end]
	rest := sig[end+1:]
	names := extractParamNames(inner)
	if len(names) == 0 {
		return sig
	}

	// Build replacement map oldName -> Tn. Also rewrite the inner text.
	repl := make(map[string]string, len(names))
	canon := make([]string, len(names))
	for n, name := range names {
		canon[n] = "T" + itoa(n)
		repl[name] = canon[n]
	}

	newInner := rewriteGenericInner(inner, repl)
	newRest := substituteIdentifiers(rest, repl)

	var b strings.Builder
	b.Grow(len(sig))
	b.WriteString(sig[:i])
	b.WriteByte(openBr)
	b.WriteString(newInner)
	b.WriteByte(closeBr)
	b.WriteString(newRest)
	return b.String()
}

// extractParamNames returns the leading identifier of each comma-separated
// entry in a type-parameter list. For `T any, U comparable | int` it
// returns ["T", "U"]. Splitting respects nested brackets so a constraint
// like `T extends Array<U>` does not bleed into the next entry. The
// function never returns duplicates — if a name repeats, only the first
// occurrence is recorded.
func extractParamNames(inner string) []string {
	parts := splitTopLevelCommas(inner)
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		name := leadingIdentifier(p)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// rewriteGenericInner applies repl to the leading identifier of each
// comma-separated entry, leaving constraints/defaults untouched except
// for whole-word substitutions. Whole-word substitution inside the inner
// text matters when one constraint refers to another type parameter
// (e.g. `K, V extends Map<K, K>`).
func rewriteGenericInner(inner string, repl map[string]string) string {
	parts := splitTopLevelCommas(inner)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, substituteIdentifiers(p, repl))
	}
	return strings.Join(out, ",")
}

// substituteIdentifiers replaces every whole-word occurrence of any key
// in repl with its mapped value. "Whole word" means flanked by non-
// identifier characters (or string boundaries). The function performs a
// single linear scan; replacement values are never re-scanned, so a
// chain repl["A"]="B", repl["B"]="C" maps "A" to "B" rather than "C".
func substituteIdentifiers(s string, repl map[string]string) string {
	if len(repl) == 0 || s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		if isIdentStart(c) {
			j := i + 1
			for j < len(s) && isIdentCont(s[j]) {
				j++
			}
			word := s[i:j]
			if mapped, ok := repl[word]; ok {
				b.WriteString(mapped)
			} else {
				b.WriteString(word)
			}
			i = j
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// splitTopLevelCommas splits s on commas that are not inside any nested
// bracket pair. Brackets recognized: `()`, `[]`, `{}`, `<>`. Empty parts
// (e.g. trailing comma) are filtered out.
func splitTopLevelCommas(s string) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{', '<':
			depth++
		case ')', ']', '}', '>':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				part := strings.TrimSpace(s[start:i])
				if part != "" {
					out = append(out, part)
				}
				start = i + 1
			}
		}
	}
	last := strings.TrimSpace(s[start:])
	if last != "" {
		out = append(out, last)
	}
	return out
}

func leadingIdentifier(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !isIdentStart(s[0]) {
		return ""
	}
	j := 1
	for j < len(s) && isIdentCont(s[j]) {
		j++
	}
	return s[:j]
}

func isIdentStart(b byte) bool {
	return b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func isIdentCont(b byte) bool {
	return isIdentStart(b) || (b >= '0' && b <= '9')
}

// itoa formats a non-negative int as decimal without dragging in
// strconv just for this single call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
