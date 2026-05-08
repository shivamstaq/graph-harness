package normalize

import "testing"

func TestGo_StableUnderFormatting(t *testing.T) {
	t.Parallel()
	cases := []struct{ a, b string }{
		{"(  a int,  b   string ) error", "(a int, b string) error"},
		{"(a int, b string) error", "( a int , b string ) error"},
		{"() error", "(  )   error"},
		{"(a int)", "(a int)"},
	}
	for _, tc := range cases {
		if got, want := Go(tc.a), Go(tc.b); got != want {
			t.Errorf("formatting changed signature: Go(%q)=%q vs Go(%q)=%q", tc.a, got, tc.b, want)
		}
	}
}

// TestGo_CanonicalForm pins the byte-exact canonical form so changes
// to the normalizer are deliberate. Whitespace is collapsed; commas
// are followed by no space; brackets hug their contents.
func TestGo_CanonicalForm(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"(a int, b string) error", "(a int,b string) error"},
		{"( a int , b string ) error", "(a int,b string) error"},
		{"(a int,b string) error", "(a int,b string) error"},
	}
	for _, tc := range cases {
		if got := Go(tc.in); got != tc.want {
			t.Errorf("Go(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestGo_GenericsCanonicalized(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"[T any](x T) T", "[T0 any](x T0) T0"},
		{"[T any, U comparable](k T, v U) (T, U)", "[T0 any,T1 comparable](k T0,v T1) (T0,T1)"},
		{"[K comparable, V any](m map[K]V) []V", "[T0 comparable,T1 any](m map[T0]T1) []T1"},
	}
	for _, tc := range cases {
		if got := Go(tc.in); got != tc.want {
			t.Errorf("Go(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestGo_GenericNamesEqualUnderRename(t *testing.T) {
	t.Parallel()
	a := Go("[T any](x T) T")
	b := Go("[Element any](x Element) Element")
	if a != b {
		t.Errorf("rename of generic param changed canonical signature: %q vs %q", a, b)
	}
}

func TestGo_NoLeadingGenericBlock(t *testing.T) {
	t.Parallel()
	// `[]T` at the start is a slice type, not a type-parameter block.
	in := "[]T error"
	got := Go(in)
	if got != "[]T error" {
		t.Errorf("non-generic leading bracket should not be rewritten: got %q", got)
	}
}

func TestTS_StableUnderFormatting(t *testing.T) {
	t.Parallel()
	a := TS("( req: Request ,  res: Response ) : void")
	b := TS("(req: Request,res: Response) : void")
	if a != b {
		t.Errorf("TS formatting normalization failed: %q vs %q", a, b)
	}
}

func TestTS_GenericsCanonicalized(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"<T>(x: T) => T", "<T0>(x: T0) => T0"},
		{"<T extends string, U = number>(t: T, u: U): U", "<T0 extends string,T1 = number>(t: T0,u: T1): T1"},
	}
	for _, tc := range cases {
		if got := TS(tc.in); got != tc.want {
			t.Errorf("TS(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestTS_GenericNamesEqualUnderRename(t *testing.T) {
	t.Parallel()
	a := TS("<T extends string>(x: T): T")
	b := TS("<Element extends string>(x: Element): Element")
	if a != b {
		t.Errorf("TS rename should not change canonical signature: %q vs %q", a, b)
	}
}

func TestPython_StableUnderFormatting(t *testing.T) {
	t.Parallel()
	a := Python("( a: int ,  b: str ) -> bool")
	b := Python("(a: int, b: str) -> bool")
	if a != b {
		t.Errorf("Python whitespace normalization failed: %q vs %q", a, b)
	}
}

func TestPython_KeywordOnlySorted(t *testing.T) {
	t.Parallel()
	cases := []struct{ a, b string }{
		{"(self, *, b: int = 1, a: str = 'x')", "(self, *, a: str = 'x', b: int = 1)"},
		{"(self, *args, b: int, a: int, **kwargs)", "(self, *args, a: int, b: int, **kwargs)"},
	}
	for _, tc := range cases {
		if got, want := Python(tc.a), Python(tc.b); got != want {
			t.Errorf("Python kw-only sorting failed: Python(%q)=%q vs Python(%q)=%q", tc.a, got, tc.b, want)
		}
	}
}

func TestPython_PositionalOrderPreserved(t *testing.T) {
	t.Parallel()
	a := Python("(a: int, b: str)")
	b := Python("(b: str, a: int)")
	if a == b {
		t.Errorf("Python should not sort positional params; got identical canonical form for %q and %q", a, b)
	}
}

func TestPython_PEP695GenericsCanonicalized(t *testing.T) {
	t.Parallel()
	a := Python("[T](xs: list[T]) -> T")
	b := Python("[Element](xs: list[Element]) -> Element")
	if a != b {
		t.Errorf("PEP 695 generic rename should normalize: %q vs %q", a, b)
	}
}

func TestForLanguage_DispatchesByID(t *testing.T) {
	t.Parallel()
	if got, want := ForLanguage("go", "[T any](x T) T"), Go("[T any](x T) T"); got != want {
		t.Errorf("ForLanguage(go) = %q; want %q", got, want)
	}
	if got, want := ForLanguage("typescript", "<T>(x: T): T"), TS("<T>(x: T): T"); got != want {
		t.Errorf("ForLanguage(typescript) = %q; want %q", got, want)
	}
	if got, want := ForLanguage("python", "(self, *, b: int, a: int)"), Python("(self, *, b: int, a: int)"); got != want {
		t.Errorf("ForLanguage(python) = %q; want %q", got, want)
	}
}

func TestForLanguage_UnknownFallsBackToWhitespace(t *testing.T) {
	t.Parallel()
	// Same source shape, different whitespace runs — the fallback only
	// canonicalizes whitespace, so equal-modulo-whitespace inputs must
	// collapse to the same canonical form.
	a := ForLanguage("rust", "fn (  a: i32 ,  b: i32  )  -> i32")
	b := ForLanguage("rust", "fn (a: i32, b: i32) -> i32")
	if a != b {
		t.Errorf("unknown-language fallback should normalize whitespace: %q vs %q", a, b)
	}
}

func TestEmpty(t *testing.T) {
	t.Parallel()
	if Go("") != "" || TS("") != "" || Python("") != "" {
		t.Errorf("empty input should produce empty output")
	}
}

func TestSplitTopLevelCommas_RespectsNesting(t *testing.T) {
	t.Parallel()
	got := splitTopLevelCommas("a, Map<K, V>, c")
	want := []string{"a", "Map<K, V>", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v; want %v", got, want)
	}
	for i, p := range got {
		if p != want[i] {
			t.Errorf("part %d = %q; want %q", i, p, want[i])
		}
	}
}

func TestSubstituteIdentifiers_WholeWordOnly(t *testing.T) {
	t.Parallel()
	repl := map[string]string{"T": "T0"}
	got := substituteIdentifiers("Map[T, Trace]", repl)
	if got != "Map[T0, Trace]" {
		t.Errorf("substituteIdentifiers should match whole words only; got %q", got)
	}
}
