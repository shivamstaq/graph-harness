package kernel

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed embedded_manifests/*.yaml
var embeddedManifests embed.FS

// WalkEmbeddedManifests iterates the embedded YAML manifest bytes in
// dependency-order-agnostic name-sorted form. Callers that only need raw
// YAML (e.g. review_queue.Queue.LoadRequirements) use this; callers that
// need parsed Manifest structs go through (*Registry).LoadEmbeddedManifests.
func WalkEmbeddedManifests(fn func(name string, data []byte) error) error {
	entries, err := embeddedManifests.ReadDir("embedded_manifests")
	if err != nil {
		return fmt.Errorf("read embedded: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		data, err := embeddedManifests.ReadFile("embedded_manifests/" + n)
		if err != nil {
			return err
		}
		if err := fn(n, data); err != nil {
			return err
		}
	}
	return nil
}

// LoadEmbeddedManifests installs every manifest baked into the binary at build
// time (mirrored from the project-root manifests/ directory). Used when the
// CLI runs outside the project tree, e.g. from `go install`.
func (r *Registry) LoadEmbeddedManifests() error {
	entries, err := embeddedManifests.ReadDir("embedded_manifests")
	if err != nil {
		return fmt.Errorf("read embedded: %w", err)
	}
	pending := make(map[string]*Manifest)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := embeddedManifests.ReadFile("embedded_manifests/" + e.Name())
		if err != nil {
			return err
		}
		m, err := ParseManifest(data)
		if err != nil {
			return fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		pending[m.Metadata.Name] = m
	}
	for len(pending) > 0 {
		progress := false
		names := make([]string, 0, len(pending))
		for n := range pending {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			m := pending[n]
			ready := true
			for _, dep := range m.Metadata.DependsOn {
				depName := strings.SplitN(dep, "@", 2)[0]
				if _, ok := r.layers[depName]; !ok {
					ready = false
					break
				}
			}
			if ready {
				if err := r.Install(m); err != nil {
					return err
				}
				delete(pending, n)
				progress = true
			}
		}
		if !progress {
			missing := make([]string, 0, len(pending))
			for n := range pending {
				missing = append(missing, n)
			}
			sort.Strings(missing)
			return fmt.Errorf("dependency cycle or missing dep among: %s", strings.Join(missing, ", "))
		}
	}
	return nil
}
