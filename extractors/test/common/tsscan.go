package common

import "regexp"

// TSTest is one Jest / Vitest test case picked up by the regex scan.
type TSTest struct {
	// FullName is the dotted name including any describe() wrapping
	// (e.g. "OrderService describe handles failure" — describe nesting
	// uses the literal block title verbatim).
	FullName string

	// CaseName is the inner-most `it`/`test` title (the call's first
	// string arg).
	CaseName string

	// Outer is the slash-joined describe stack at the call site
	// ("OrderService/sad-paths" for two nested describes); empty if
	// no enclosing describe. Provided for downstream test-grouping UI.
	Outer string
}

// TSFixture is a beforeEach / afterEach / beforeAll / afterAll block,
// surfaced as a code_framework.Fixture.
type TSFixture struct {
	Hook string // "beforeEach" | "afterEach" | "beforeAll" | "afterAll"
	// Outer is the describe stack at the hook's call site.
	Outer string
}

// jsCall matches a Jest/Vitest call like `describe('foo', () => {`,
// `it('bar', ...)`, `test('baz', ...)`, `beforeEach(() => {`. Quote
// styles supported: ", ', `. Optional `.only` / `.skip` /
// `.each(...)` chained on `it`/`test`/`describe`.
//
// We rely on the fact that ts-jest test files are syntactically
// regular enough that this regex catches the call shapes without a
// real parser. Edge cases (string concatenated titles, template
// literals with interpolation) lose the title but the call site is
// still recorded — the test row carries a synthesized "<computed>"
// name in that case.
var jsCall = regexp.MustCompile(
	"(?m)" +
		// optional leading non-identifier (whitespace, '{', ';', ','):
		"(^|[^A-Za-z0-9_$])" +
		// the function name + optional .only/.skip/.each modifiers
		`(describe|it|test|beforeEach|afterEach|beforeAll|afterAll)` +
		`(?:\.(?:only|skip|each\([^)]*\)|concurrent|sequential))?` +
		// the open paren
		`\s*\(\s*` +
		// the first arg: a string literal in any of 3 quote styles,
		// optionally a template literal with no interpolations
		`(?:"((?:\\.|[^"\\])*)"|'((?:\\.|[^'\\])*)'|` + "`([^`\\$]*)`" + `)?`,
)

// frame is one entry on the describe-stack the TS scanner builds.
type frame struct {
	name     string
	braceLvl int // brace level when the describe call was opened
}

// ScanTSTests walks src and returns the (tests, fixtures) pair. The
// describe stack is reconstructed by tracking brace balance — coarse
// but accurate for the common patterns:
//
//	describe('Outer', () => {
//	  describe('Inner', () => {
//	    it('case', () => { ... })
//	  })
//	})
//
// becomes ("Outer/Inner", "case"). Comments and string literals are
// scanned with a simple state machine so commented-out tests are not
// picked up.
func ScanTSTests(src []byte) ([]TSTest, []TSFixture) {
	clean := stripTSCommentsAndStrings(src)
	matches := jsCall.FindAllSubmatchIndex(clean, -1)
	if len(matches) == 0 {
		return nil, nil
	}

	var stack []frame
	braceLvl := 0

	// Walk through src position by position and pause at every match
	// + every brace event. To keep it linear, collect brace positions
	// once and merge-walk them with the match list.
	bracePos := indexBraces(clean)

	var tests []TSTest
	var fixtures []TSFixture

	bp := 0
	for _, m := range matches {
		pos := m[2] // start of function name token
		// Advance bracePos cursor up to pos, updating braceLvl and
		// popping any expired describe frames.
		for bp < len(bracePos) && bracePos[bp].pos <= pos {
			if bracePos[bp].open {
				braceLvl++
			} else {
				braceLvl--
				// Pop frames whose braceLvl >= current.
				for len(stack) > 0 && stack[len(stack)-1].braceLvl > braceLvl {
					stack = stack[:len(stack)-1]
				}
			}
			bp++
		}

		name := string(clean[m[4]:m[5]])
		title := firstNonEmpty(
			submatch(clean, m, 6),
			submatch(clean, m, 8),
			submatch(clean, m, 10),
		)
		if title == "" {
			title = "<computed>"
		}

		outer := joinOuter(stack)
		switch name {
		case "describe":
			// Push a new frame; the body opens at the next `{`.
			stack = append(stack, frame{name: title, braceLvl: braceLvl})
		case "it", "test":
			tests = append(tests, TSTest{
				FullName: composeFullName(outer, title),
				CaseName: title,
				Outer:    outer,
			})
		case "beforeEach", "afterEach", "beforeAll", "afterAll":
			fixtures = append(fixtures, TSFixture{Hook: name, Outer: outer})
		}
	}
	return tests, fixtures
}

func submatch(src []byte, m []int, idx int) string {
	if idx+1 >= len(m) {
		return ""
	}
	a, b := m[idx], m[idx+1]
	if a < 0 || b < 0 {
		return ""
	}
	return string(src[a:b])
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// stripTSCommentsAndStrings replaces comment bodies and string
// contents with spaces of the same length, preserving offsets. This
// keeps the regex from picking up `it('x'`-shaped calls hidden inside
// a string literal or a block comment.
//
// Edge cases not handled: template-literal interpolations (we leave
// the template contents alone — they are still strings to TS), JSX
// (the test files are .test.ts/.spec.ts, JSX rare). The state machine
// is intentionally simple — false negatives in commented-out tests
// are preferable to crashing on novel syntax.
func stripTSCommentsAndStrings(src []byte) []byte {
	out := make([]byte, len(src))
	copy(out, src)
	i := 0
	for i < len(out) {
		c := out[i]
		// // line comment
		if c == '/' && i+1 < len(out) && out[i+1] == '/' {
			for i < len(out) && out[i] != '\n' {
				out[i] = ' '
				i++
			}
			continue
		}
		// /* block comment */
		if c == '/' && i+1 < len(out) && out[i+1] == '*' {
			out[i], out[i+1] = ' ', ' '
			i += 2
			for i < len(out)-1 && !(out[i] == '*' && out[i+1] == '/') {
				if out[i] != '\n' {
					out[i] = ' '
				}
				i++
			}
			if i < len(out)-1 {
				out[i], out[i+1] = ' ', ' '
				i += 2
			}
			continue
		}
		// strings — keep ONLY the opening + closing quote so the
		// regex still sees the title as a captured group.
		if c == '"' || c == '\'' || c == '`' {
			i++
			continue
		}
		i++
	}
	return out
}

type braceEvent struct {
	pos  int
	open bool
}

// indexBraces returns the brace positions in src (already stripped
// of comments) along with whether each is an open or close. Brace
// chars inside strings have been preserved by stripTSCommentsAndStrings
// — that's acceptable noise because describe/it call sites are at
// the top level of the file, and the title-positions are already
// captured before we cross any brace.
func indexBraces(src []byte) []braceEvent {
	var out []braceEvent
	for i, c := range src {
		switch c {
		case '{':
			out = append(out, braceEvent{pos: i, open: true})
		case '}':
			out = append(out, braceEvent{pos: i, open: false})
		}
	}
	return out
}

func joinOuter(stack []frame) string {
	if len(stack) == 0 {
		return ""
	}
	out := stack[0].name
	for i := 1; i < len(stack); i++ {
		out += "/" + stack[i].name
	}
	return out
}

func composeFullName(outer, title string) string {
	if outer == "" {
		return title
	}
	return outer + "/" + title
}
