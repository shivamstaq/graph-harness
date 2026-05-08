package lsp

import (
	"os/exec"
	"sync"
)

// Registry tracks which Driver factories are known and which of their
// underlying executables are discoverable on $PATH. The Host calls
// Registry.Available to enumerate languages that are usable in the
// current environment — gracefully degrades when (e.g.) typescript-
// language-server isn't installed.
//
// Custom drivers can be registered via Register; this is the
// extension point for site-specific language servers without
// modifying core.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]registryEntry
}

type registryEntry struct {
	executable string
	factory    func() Driver
}

// NewRegistry returns a Registry pre-populated with the three v1
// drivers (gopls, typescript-language-server, pyright-langserver).
func NewRegistry() *Registry {
	r := &Registry{factories: make(map[string]registryEntry)}
	r.Register("go", "gopls", NewGoplsDriver)
	r.Register("typescript", "typescript-language-server", NewTSServerDriver)
	r.Register("python", "pyright-langserver", NewPyrightDriver)
	return r
}

// Register adds (or replaces) a driver factory. languageID is the
// canonical id source_live.LanguageOf returns; executable is the
// $PATH lookup key used to detect whether this driver is usable.
func (r *Registry) Register(languageID, executable string, factory func() Driver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[languageID] = registryEntry{executable: executable, factory: factory}
}

// Available reports the set of language ids whose drivers are usable
// in the current environment (the executable is on $PATH).
func (r *Registry) Available() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.factories))
	for lang, ent := range r.factories {
		if _, err := exec.LookPath(ent.executable); err == nil {
			out = append(out, lang)
		}
	}
	return out
}

// HasLanguage reports whether languageID has a registered driver,
// regardless of whether the executable is currently on $PATH.
func (r *Registry) HasLanguage(languageID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.factories[languageID]
	return ok
}

// LookupExecutable returns the path the executable resolves to on the
// current $PATH, plus a usable bool. Useful for the doctor / surface
// layers that report environmental readiness.
func (r *Registry) LookupExecutable(languageID string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ent, ok := r.factories[languageID]
	if !ok {
		return "", false
	}
	path, err := exec.LookPath(ent.executable)
	if err != nil {
		return "", false
	}
	return path, true
}

// build returns a fresh Driver for languageID, or nil if no factory
// is registered. The Host calls this lazily on first request.
func (r *Registry) build(languageID string) Driver {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ent, ok := r.factories[languageID]
	if !ok {
		return nil
	}
	return ent.factory()
}
