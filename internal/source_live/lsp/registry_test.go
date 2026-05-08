package lsp

import "testing"

func TestRegistry_RegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	if !r.HasLanguage("go") {
		t.Errorf("registry should know about go by default")
	}
	if !r.HasLanguage("typescript") {
		t.Errorf("registry should know about typescript by default")
	}
	if !r.HasLanguage("python") {
		t.Errorf("registry should know about python by default")
	}
	if r.HasLanguage("rust") {
		t.Errorf("registry should not know about rust without registration")
	}
	r.Register("rust", "rust-analyzer", func() Driver { return nil })
	if !r.HasLanguage("rust") {
		t.Errorf("registry should know about rust after registration")
	}
}

func TestRegistry_AvailableSubsetOnPath(t *testing.T) {
	r := NewRegistry()
	r.Register("imaginary", "definitely-not-on-path-9001", func() Driver { return nil })
	for _, lang := range r.Available() {
		if lang == "imaginary" {
			t.Errorf("Available() returned a language whose executable is not on $PATH")
		}
	}
}

func TestRegistry_LookupExecutable(t *testing.T) {
	r := NewRegistry()
	r.Register("imaginary", "definitely-not-on-path-9001", func() Driver { return nil })
	if _, ok := r.LookupExecutable("imaginary"); ok {
		t.Errorf("LookupExecutable should report missing executable")
	}
	if _, ok := r.LookupExecutable("nope"); ok {
		t.Errorf("LookupExecutable on unregistered language should report not found")
	}
}
