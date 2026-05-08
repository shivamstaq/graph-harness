package scip

import (
	"strings"
)

// SCIP symbol-string parsing. The SCIP spec defines a stable, human-
// readable grammar for symbol identifiers: a sequence of descriptors
// suffixed with a single character that encodes the descriptor kind
// (`#` Type, `.` Term, `(...)` Method, etc.). See scip.proto's
// `Symbol` and `Descriptor` messages for the canonical grammar; the
// parser here is the §6.12-relevant subset.
//
// Example symbols:
//
//	scip-go gomod github.com/foo/bar v1 `pkg/checkout`/Validator#Validate().
//	scip-typescript npm @scope/foo 1.0 `src/auth`/CheckoutValidator#validate().
//	scip-python . . . sample.checkout/CheckoutValidator#validate().
//
// We parse out:
//   - scheme  (e.g. "scip-go", "scip-typescript", "scip-python")
//   - package (manager + name + version, opaque to us)
//   - descriptors[] with name + suffix
//
// From these we derive QualifiedName + Receiver + Kind in the
// per-language importers.

// descriptorSuffix mirrors scip.proto Descriptor.Suffix.
type descriptorSuffix string

// Suffix constants. The trailing token in a SCIP symbol descriptor
// maps 1:1 here.
const (
	suffixNamespace     descriptorSuffix = "namespace" // `/`
	suffixType          descriptorSuffix = "type"      // `#`
	suffixTerm          descriptorSuffix = "term"      // `.`
	suffixMethod        descriptorSuffix = "method"    // `().`
	suffixTypeParameter descriptorSuffix = "typeparam" // `[name]`
	suffixParameter     descriptorSuffix = "parameter" // `(name)`
	suffixMeta          descriptorSuffix = "meta"      // `:`
	suffixMacro         descriptorSuffix = "macro"     // `!`
)

// descriptor is one segment of a SCIP symbol string.
type descriptor struct {
	name          string
	disambiguator string
	suffix        descriptorSuffix
}

// parsedSymbol is the parsed form of a SCIP symbol id.
type parsedSymbol struct {
	scheme      string
	pkgManager  string
	pkgName     string
	pkgVersion  string
	descriptors []descriptor
	local       string // non-empty for `local <id>` symbols
}

// parseSymbol decodes a SCIP symbol-id string. Returns nil for an
// empty input (which SCIP indexers emit for occurrences without an
// associated symbol).
//
// The SCIP spec defines the grammar in scip.proto:
//
//	<scheme> ' ' <package> ' ' <descriptor>+ | 'local ' <local-id>
//	<package>     ::= <manager> ' ' <name> ' ' <version>
//	<descriptor>  ::= <name> <disambiguator>? <suffix>
//	<suffix>      ::= '/' | '#' | '.' | '()' | '[]' | ':' | '!'
//
// Spaces inside backtick-quoted names are part of the name.
func parseSymbol(s string) *parsedSymbol {
	if s == "" {
		return nil
	}
	if rest, ok := strings.CutPrefix(s, "local "); ok {
		return &parsedSymbol{local: rest}
	}
	scanner := &symScanner{src: s}
	scheme, ok := scanner.takeUntilSpace()
	if !ok {
		return nil
	}
	manager, ok := scanner.takeUntilSpace()
	if !ok {
		return nil
	}
	name, ok := scanner.takeUntilSpace()
	if !ok {
		return nil
	}
	version, ok := scanner.takeUntilSpace()
	if !ok {
		return nil
	}
	out := &parsedSymbol{
		scheme:     scheme,
		pkgManager: manager,
		pkgName:    name,
		pkgVersion: version,
	}
	for !scanner.eof() {
		d, ok := scanner.descriptor()
		if !ok {
			break
		}
		out.descriptors = append(out.descriptors, d)
	}
	return out
}

// qualifiedName reconstructs a dotted qualified name from the
// descriptors. Method-disambiguation suffixes (`()` plus optional
// disambiguator) are dropped from the output — the qualified name is
// the language-level identifier, not the SCIP-symbol identifier.
func (p *parsedSymbol) qualifiedName() string {
	if p == nil || len(p.descriptors) == 0 {
		return ""
	}
	parts := make([]string, 0, len(p.descriptors))
	for _, d := range p.descriptors {
		if d.suffix == suffixTypeParameter || d.suffix == suffixParameter || d.suffix == suffixMeta {
			continue
		}
		parts = append(parts, d.name)
	}
	return strings.Join(parts, ".")
}

// methodReceiver returns the qualified name of the type immediately
// preceding a Method descriptor, or "" if the symbol is not a method.
func (p *parsedSymbol) methodReceiver() string {
	if p == nil {
		return ""
	}
	for i, d := range p.descriptors {
		if d.suffix == suffixMethod && i > 0 {
			parts := make([]string, 0, i)
			for _, prev := range p.descriptors[:i] {
				if prev.suffix == suffixTypeParameter || prev.suffix == suffixParameter {
					continue
				}
				parts = append(parts, prev.name)
			}
			return strings.Join(parts, ".")
		}
	}
	return ""
}

// terminalSuffix returns the suffix of the last descriptor, or "".
// Useful for inferring symbol kind when SymbolInformation.kind is
// unset (older indexers).
func (p *parsedSymbol) terminalSuffix() descriptorSuffix {
	if p == nil || len(p.descriptors) == 0 {
		return ""
	}
	return p.descriptors[len(p.descriptors)-1].suffix
}

// terminalName returns the last descriptor's local name. SCIP encodes
// the symbol's own identifier as the final descriptor (e.g. `Validate`
// in `pkg/Validator#Validate().`).
func (p *parsedSymbol) terminalName() string {
	if p == nil || len(p.descriptors) == 0 {
		return ""
	}
	return p.descriptors[len(p.descriptors)-1].name
}

// symScanner walks a SCIP symbol string producing descriptors.
type symScanner struct {
	src string
	pos int
}

func (s *symScanner) eof() bool { return s.pos >= len(s.src) }

// takeUntilSpace consumes characters up to the next space, returning
// the token and advancing past the space. Backtick-quoted spans are
// treated as opaque.
func (s *symScanner) takeUntilSpace() (string, bool) {
	if s.eof() {
		return "", false
	}
	start := s.pos
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		switch c {
		case '`':
			// Skip to the matching backtick.
			s.pos++
			for s.pos < len(s.src) && s.src[s.pos] != '`' {
				s.pos++
			}
			if s.pos < len(s.src) {
				s.pos++ // consume closing backtick
			}
		case ' ':
			tok := s.src[start:s.pos]
			s.pos++ // consume space
			return tok, true
		default:
			s.pos++
		}
	}
	if s.pos > start {
		return s.src[start:], true
	}
	return "", false
}

// descriptor consumes one descriptor (name + optional disambiguator
// + suffix). The textual descriptor grammar (per scip.md):
//
//	namespace      ::= <name> '/'
//	type           ::= <name> '#'
//	term           ::= <name> '.'
//	method         ::= <name> '(' (<disambiguator>)? ').'
//	type-parameter ::= '[' <name> ']'
//	parameter      ::= '(' <name> ')'
//	meta           ::= <name> ':'
//	macro          ::= <name> '!'
//
// Returns ok=false at end of string.
func (s *symScanner) descriptor() (descriptor, bool) {
	if s.eof() {
		return descriptor{}, false
	}
	d := descriptor{name: s.descriptorName()}
	if s.eof() {
		// No suffix means we ran out of input mid-descriptor; treat
		// as an unfinished descriptor and stop.
		return d, d.name != ""
	}
	c := s.src[s.pos]
	switch c {
	case '/':
		s.pos++
		d.suffix = suffixNamespace
	case '#':
		s.pos++
		d.suffix = suffixType
	case '.':
		s.pos++
		d.suffix = suffixTerm
	case ':':
		s.pos++
		d.suffix = suffixMeta
	case '!':
		s.pos++
		d.suffix = suffixMacro
	case '(':
		// method (with optional disambiguator) or parameter.
		s.pos++ // consume '('
		end := indexAtDepth(s.src[s.pos:], ')', '(')
		if end < 0 {
			return d, false
		}
		d.disambiguator = s.src[s.pos : s.pos+end]
		s.pos += end + 1 // past ')'
		// A trailing '.' marks the method suffix; otherwise it's a
		// (rare) parameter descriptor.
		if !s.eof() && s.src[s.pos] == '.' {
			s.pos++
			d.suffix = suffixMethod
		} else {
			d.suffix = suffixParameter
		}
	case '[':
		s.pos++
		end := indexAtDepth(s.src[s.pos:], ']', '[')
		if end < 0 {
			return d, false
		}
		d.name = s.src[s.pos : s.pos+end]
		s.pos += end + 1
		d.suffix = suffixTypeParameter
	default:
		return d, false
	}
	return d, true
}

// indexAtDepth returns the offset of the first `close` at the same
// nesting level that `open` introduces. Returns -1 if not found.
func indexAtDepth(s string, closeCh, openCh byte) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case openCh:
			depth++
		case closeCh:
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return -1
}

// descriptorName reads a descriptor name. SCIP names can be backtick-
// quoted to embed spaces / suffix-significant characters; otherwise a
// name terminates at the first suffix character.
func (s *symScanner) descriptorName() string {
	if s.src[s.pos] == '`' {
		s.pos++
		start := s.pos
		for s.pos < len(s.src) && s.src[s.pos] != '`' {
			s.pos++
		}
		name := s.src[start:s.pos]
		if s.pos < len(s.src) {
			s.pos++
		}
		return name
	}
	start := s.pos
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		switch c {
		case '/', '#', '.', '(', ')', '[', ']', ':', '!':
			return s.src[start:s.pos]
		}
		s.pos++
	}
	return s.src[start:s.pos]
}
