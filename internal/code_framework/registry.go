package code_framework

import (
	"fmt"
	"sort"
	"sync"

	"github.com/shivamstaq/graph-harness/internal/facts"
)

// Constructor builds an Extractor instance given the dispatcher's
// shared dependencies. Called once per (workspace, extractor name)
// when the Dispatcher starts.
type Constructor func(Deps) (Extractor, error)

// Deps is the set of dependencies the Dispatcher hands each
// extractor at construction time. Extractors should not hold their
// own facts.Facts or *Store handles outside what Deps provides.
type Deps struct {
	// Facts is the kernel-internal storage contract (SPEC §6.4).
	// Extractors use it to read code.core state via Kernel.Route
	// (selectors are the only legal cross-layer reference) — they
	// do not write through it; writes flow through the Dispatcher
	// which appends to the EventLog.
	Facts facts.Facts

	// RefCache resolves SelectorRefs to EntityRefs at extract time
	// (P2.T04). Shared across extractors within one Dispatcher so
	// duplicate resolutions are deduplicated.
	RefCache *EntityRefCache

	// Workspace is the absolute path to the workspace root. Used by
	// extractors that need to read source files directly (the
	// tree-sitter-only fallback path).
	Workspace string

	// Logf is a structured-log entry point. Format string + args
	// follow the standard fmt.Sprintf convention.
	Logf func(string, ...any)
}

// Descriptor names a registered extractor at the package level.
// Surfaced by ExtractorsList JSON-RPC without instantiating the
// extractor.
type Descriptor struct {
	Name         string       `json:"name"`
	Family       string       `json:"family"`
	Languages    []string     `json:"languages"`
	Frameworks   []string     `json:"frameworks"`
	Fallback     FallbackMode `json:"fallback"`
	BatchHint    BatchMode    `json:"batch_hint"`
	Inputs       []EventKind  `json:"inputs"`
	Outputs      []EntityKind `json:"outputs"`
}

// reg is the package-level registry populated by init() in each
// extractor package (one init() per family-language subdir).
//
// Compile-time activation: the daemon imports an aggregator package
// (see /home/shivam/Work/tries/graph-harness/extractors/all.go in P1)
// whose only purpose is blank-import every extractor subdir. Disabling
// an extractor at *build* time removes the blank import. Disabling at
// *runtime* uses Dispatcher.Disable.
type registryEntry struct {
	name string
	ctor Constructor
	desc Descriptor
}

var (
	regMu sync.RWMutex
	reg   = map[string]registryEntry{}
)

// Register installs an extractor constructor under name. Idempotent
// for identical entries; panics if the same name is registered with
// a different constructor (programmer error caught at boot).
//
// Typical use, called from each extractor package's init():
//
//	func init() {
//	    code_framework.Register("routes.go.chi", newChiRoutesExtractor)
//	}
//
// `desc` is the static descriptor for the extractor — the same data
// the extractor would return from Capabilities()/Inputs()/Outputs(),
// surfaced so `extractors list` works without instantiating the
// extractor.
func Register(name string, ctor Constructor, desc Descriptor) {
	if name == "" {
		panic("code_framework.Register: name must be non-empty")
	}
	if ctor == nil {
		panic("code_framework.Register: ctor must be non-nil")
	}
	regMu.Lock()
	defer regMu.Unlock()
	if existing, ok := reg[name]; ok {
		// Idempotent re-registration with the same ctor pointer.
		if fmt.Sprintf("%p", existing.ctor) == fmt.Sprintf("%p", ctor) {
			return
		}
		panic(fmt.Sprintf("code_framework.Register: %q already registered with a different constructor", name))
	}
	desc.Name = name
	reg[name] = registryEntry{name: name, ctor: ctor, desc: desc}
}

// Registered returns the sorted list of registered extractor names.
func Registered() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(reg))
	for name := range reg {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Lookup returns the constructor for name, plus its static descriptor.
func Lookup(name string) (Constructor, Descriptor, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	e, ok := reg[name]
	if !ok {
		return nil, Descriptor{}, false
	}
	return e.ctor, e.desc, true
}

// Descriptors returns the static descriptors for every registered
// extractor, sorted by Name.
func Descriptors() []Descriptor {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Descriptor, 0, len(reg))
	for _, e := range reg {
		out = append(out, e.desc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ResetRegistryForTest clears the registry. Test-only: production
// code never resets the registry. Exposed so the gate test can
// install fakes without interference from blank-imported real
// extractors.
func ResetRegistryForTest() {
	regMu.Lock()
	defer regMu.Unlock()
	reg = map[string]registryEntry{}
}
