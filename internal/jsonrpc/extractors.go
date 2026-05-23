package jsonrpc

import (
	"context"
	"fmt"

	"github.com/shivamstaq/graph-harness/internal/code_framework"
)

// ExtractorsListResult is the response envelope for extractors.list.
// Returns the static descriptors of every registered extractor —
// independent of whether a Dispatcher is wired (so the one-shot batch
// CLI path can list available extractors without spawning the daemon).
type ExtractorsListResult struct {
	Extractors []code_framework.Descriptor `json:"extractors"`
}

// ExtractorsList returns the package-level registry's descriptors.
func (s *Service) ExtractorsList(_ context.Context) (ExtractorsListResult, error) {
	return ExtractorsListResult{Extractors: code_framework.Descriptors()}, nil
}

// ExtractorsStatusParams optionally filters the live status snapshot
// by extractor name. Empty Names → return everything.
type ExtractorsStatusParams struct {
	Names []string `json:"names,omitempty"`
}

// ExtractorsStatusResult is the response envelope for extractors.status.
type ExtractorsStatusResult struct {
	Extractors []code_framework.ExtractorStatus `json:"extractors"`
}

// ExtractorsStatus returns the live status snapshot.
//
// When no Dispatcher is wired (batch path), returns descriptor-derived
// rows with Enabled=true and zero counters so callers can still see
// the static catalog. The CLI prints this without crashing.
func (s *Service) ExtractorsStatus(_ context.Context, p ExtractorsStatusParams) (ExtractorsStatusResult, error) {
	d := s.Extractors()
	if d == nil {
		out := make([]code_framework.ExtractorStatus, 0, len(code_framework.Descriptors()))
		for _, desc := range code_framework.Descriptors() {
			if !nameMatches(desc.Name, p.Names) {
				continue
			}
			out = append(out, code_framework.ExtractorStatus{
				Name:       desc.Name,
				Family:     desc.Family,
				Languages:  desc.Languages,
				Frameworks: desc.Frameworks,
				Inputs:     desc.Inputs,
				Outputs:    desc.Outputs,
				Enabled:    true,
			})
		}
		return ExtractorsStatusResult{Extractors: out}, nil
	}
	all := d.Status()
	if len(p.Names) == 0 {
		return ExtractorsStatusResult{Extractors: all}, nil
	}
	out := make([]code_framework.ExtractorStatus, 0, len(all))
	for _, st := range all {
		if !nameMatches(st.Name, p.Names) {
			continue
		}
		out = append(out, st)
	}
	return ExtractorsStatusResult{Extractors: out}, nil
}

// ExtractorsEnableParams names the extractor to enable.
type ExtractorsEnableParams struct {
	Name string `json:"name"`
}

// ExtractorsEnableResult is empty; the JSON-RPC response just confirms
// success.
type ExtractorsEnableResult struct{}

// ExtractorsEnable enables an extractor at runtime + persists to the
// workspace TOML.
func (s *Service) ExtractorsEnable(ctx context.Context, p ExtractorsEnableParams) (ExtractorsEnableResult, error) {
	if p.Name == "" {
		return ExtractorsEnableResult{}, fmt.Errorf("name required")
	}
	d := s.Extractors()
	if d == nil {
		// Persist the choice even without a live dispatcher so a
		// daemon restart picks it up.
		ws := ""
		if s.Workspace != nil {
			ws = s.Workspace.Root
		}
		if ws == "" {
			return ExtractorsEnableResult{}, fmt.Errorf("no workspace; cannot persist enable")
		}
		err := code_framework.WithLockedConfig(ws, func(c *code_framework.Config) error {
			c.Enable(p.Name)
			return nil
		})
		return ExtractorsEnableResult{}, err
	}
	if err := d.Enable(ctx, p.Name); err != nil {
		return ExtractorsEnableResult{}, err
	}
	return ExtractorsEnableResult{}, nil
}

// ExtractorsDisableParams names the extractor to disable.
type ExtractorsDisableParams struct {
	Name string `json:"name"`
}

// ExtractorsDisableResult is empty; the JSON-RPC response just confirms
// success.
type ExtractorsDisableResult struct{}

// ExtractorsDisable disables an extractor at runtime + persists.
func (s *Service) ExtractorsDisable(ctx context.Context, p ExtractorsDisableParams) (ExtractorsDisableResult, error) {
	if p.Name == "" {
		return ExtractorsDisableResult{}, fmt.Errorf("name required")
	}
	d := s.Extractors()
	if d == nil {
		ws := ""
		if s.Workspace != nil {
			ws = s.Workspace.Root
		}
		if ws == "" {
			return ExtractorsDisableResult{}, fmt.Errorf("no workspace; cannot persist disable")
		}
		err := code_framework.WithLockedConfig(ws, func(c *code_framework.Config) error {
			c.Disable(p.Name)
			return nil
		})
		return ExtractorsDisableResult{}, err
	}
	if err := d.Disable(ctx, p.Name); err != nil {
		return ExtractorsDisableResult{}, err
	}
	return ExtractorsDisableResult{}, nil
}

func nameMatches(name string, filter []string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, f := range filter {
		if f == name {
			return true
		}
	}
	return false
}
