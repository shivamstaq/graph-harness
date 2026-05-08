package source_live

import "testing"

func TestParseTypeScriptFile_FunctionsAndMethods(t *testing.T) {
	src := []byte(`export class CheckoutValidator {
  validate(cart: Cart): boolean {
    return true;
  }
}

export function helper(x: number): string {
  return String(x);
}

export const arrow = (n: number): number => n * 2;
`)
	pf, err := ParseTypeScriptFile("validator.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if pf.Language != "typescript" {
		t.Errorf("language = %q, want typescript", pf.Language)
	}
	want := map[string]string{
		"validator.CheckoutValidator.validate": "CheckoutValidator",
		"validator.helper":                     "",
		"validator.arrow":                      "",
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

func TestParseTypeScriptFile_TSXSyntax(t *testing.T) {
	src := []byte(`export function Component(): JSX.Element {
  return <div>hi</div>;
}
`)
	pf, err := ParseTypeScriptFile("component.tsx", src)
	if err != nil {
		t.Fatalf("parse tsx: %v", err)
	}
	if len(pf.Functions) != 1 {
		t.Fatalf("got %d functions, want 1: %+v", len(pf.Functions), pf.Functions)
	}
	if pf.Functions[0].Name != "Component" {
		t.Errorf("name = %q, want Component", pf.Functions[0].Name)
	}
}

func TestParseTypeScriptFile_EmptyInput(t *testing.T) {
	pf, err := ParseTypeScriptFile("empty.ts", []byte{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pf.Functions) != 0 {
		t.Errorf("got %d functions, want 0", len(pf.Functions))
	}
}

func TestParseTypeScriptFile_TolerantOfSyntaxError(t *testing.T) {
	src := []byte(`export function ok(): void {}
this is not TypeScript`)
	pf, err := ParseTypeScriptFile("bad.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pf.Functions) == 0 {
		t.Errorf("expected at least the ok() function despite trailing garbage")
	}
}
