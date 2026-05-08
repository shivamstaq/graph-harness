package source_live

import "testing"

func TestParsePythonFile_FunctionsAndMethods(t *testing.T) {
	src := []byte(`"""Validator module."""

class CheckoutValidator:
    """Validates cart state."""

    def validate(self, cart) -> bool:
        return True

def helper(x: int) -> str:
    return str(x)
`)
	pf, err := ParsePythonFile("validator.py", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if pf.Language != "python" {
		t.Errorf("language = %q, want python", pf.Language)
	}
	want := map[string]string{
		"validator.CheckoutValidator.validate": "CheckoutValidator",
		"validator.helper":                     "",
	}
	if len(pf.Functions) != len(want) {
		t.Fatalf("got %d functions, want %d: %+v", len(pf.Functions), len(want), pf.Functions)
	}
	for _, fn := range pf.Functions {
		recv, ok := want[fn.QualifiedName]
		if !ok {
			t.Errorf("unexpected function %q", fn.QualifiedName)
			continue
		}
		if fn.Receiver != recv {
			t.Errorf("receiver for %s = %q, want %q", fn.QualifiedName, fn.Receiver, recv)
		}
		delete(want, fn.QualifiedName)
	}
	for n := range want {
		t.Errorf("missing function %q", n)
	}
}

func TestParsePythonFile_DecoratedFunction(t *testing.T) {
	src := []byte(`import functools

@functools.cache
def cached_lookup(key: str) -> str:
    return key.upper()
`)
	pf, err := ParsePythonFile("util.py", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pf.Functions) != 1 {
		t.Fatalf("got %d functions, want 1: %+v", len(pf.Functions), pf.Functions)
	}
	if pf.Functions[0].Name != "cached_lookup" {
		t.Errorf("name = %q, want cached_lookup", pf.Functions[0].Name)
	}
}

func TestParsePythonFile_NestedClass(t *testing.T) {
	src := []byte(`class Outer:
    class Inner:
        def method(self) -> None:
            return None
`)
	pf, err := ParsePythonFile("nested.py", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pf.Functions) != 1 {
		t.Fatalf("got %d functions, want 1: %+v", len(pf.Functions), pf.Functions)
	}
	got := pf.Functions[0]
	if got.QualifiedName != "nested.Outer.Inner.method" {
		t.Errorf("qualified name = %q, want nested.Outer.Inner.method", got.QualifiedName)
	}
	if got.Receiver != "Outer.Inner" {
		t.Errorf("receiver = %q, want Outer.Inner", got.Receiver)
	}
}

func TestParsePythonFile_EmptyInput(t *testing.T) {
	pf, err := ParsePythonFile("empty.py", []byte{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pf.Functions) != 0 {
		t.Errorf("got %d functions, want 0", len(pf.Functions))
	}
}

func TestParsePythonFile_TolerantOfSyntaxError(t *testing.T) {
	src := []byte(`def ok(): pass
this is ::: not python
`)
	pf, err := ParsePythonFile("bad.py", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pf.Functions) == 0 {
		t.Errorf("expected at least the ok() function despite trailing garbage")
	}
}
