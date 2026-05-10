package detect

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Registry tracks the per-language detectors. Mirrors the lsp.Registry
// shape so call sites can inspect available languages, look up a
// detector for a given language id, and probe every registered
// language with one call.
type Registry struct {
	mu        sync.RWMutex
	detectors map[string]Detector
}

// NewRegistry returns a Registry pre-populated with the v1 language
// detectors (Go, TypeScript, Python). Additional languages register at
// startup via Register; tests inject fakes the same way.
func NewRegistry() *Registry {
	r := &Registry{detectors: make(map[string]Detector)}
	r.Register(NewGoDetector())
	r.Register(NewTypeScriptDetector())
	r.Register(NewPythonDetector())
	return r
}

// Register adds (or replaces) a Detector. languageID comes from the
// detector itself.
func (r *Registry) Register(d Detector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.detectors[d.LanguageID()] = d
}

// LookUp returns the Detector for languageID, or nil if none is
// registered.
func (r *Registry) LookUp(languageID string) Detector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.detectors[languageID]
}

// Languages returns every registered language id, sorted, so doctor
// output is deterministic.
func (r *Registry) Languages() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.detectors))
	for lang := range r.detectors {
		out = append(out, lang)
	}
	sort.Strings(out)
	return out
}

// ProbeAll invokes every registered Detector.Probe and returns the
// per-language Reports in language-id order. Errors from individual
// detectors do not abort the sweep; they are wrapped in the report's
// first ToolReport with Status = StatusMissing and a Reason in the
// probe chain.
func (r *Registry) ProbeAll(ctx context.Context, workspaceRoot string) ([]Report, error) {
	languages := r.Languages()
	out := make([]Report, 0, len(languages))
	for _, lang := range languages {
		d := r.LookUp(lang)
		if d == nil {
			continue
		}
		report, err := d.Probe(ctx, workspaceRoot)
		if err != nil {
			out = append(out, Report{
				LanguageID: lang,
				Tools: []ToolReport{{
					Name:   "<probe-error>",
					Class:  ToolClassLSP,
					Status: StatusMissing,
					ProbeChain: []ProbeStep{{
						Location: "(detector)",
						Found:    false,
						Reason:   fmt.Sprintf("probe failed: %v", err),
					}},
				}},
			})
			continue
		}
		out = append(out, report)
	}
	return out, nil
}

// FilterLanguages narrows the registered set to the union of languages
// detected as present in the workspace (via WorkspaceLanguages) and any
// extra ids the caller wants to force-include. Used by doctor when
// --language=<id> is passed.
func (r *Registry) FilterLanguages(allowed []string) []string {
	if len(allowed) == 0 {
		return r.Languages()
	}
	allow := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		allow[a] = true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.detectors))
	for lang := range r.detectors {
		if allow[lang] {
			out = append(out, lang)
		}
	}
	sort.Strings(out)
	return out
}
