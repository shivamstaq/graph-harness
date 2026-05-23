package tui

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/shivamstaq/graph-harness/internal/change_process"
)

// ImpactPanel is the P2.T39 "live impact" cockpit panel. It maintains
// three live columns:
//
//   - Touched:   code.core entities the staged diff touches (derived by
//                projecting `git diff HEAD --name-only` through
//                Store.ListEntitiesByPath).
//   - Impacted:  framework-edge dependents flagged by stage-5
//                computeImpactedSet — surfaced as the
//                missing_dependent_update findings' FrameworkContext
//                dependents, grouped by kind.
//   - Findings:  the active missing_dependent_update findings.
//
// Update path is push-driven: the panel subscribes to a
// `code.framework` event channel (kernel.subscribe via
// facts.EventLog.SubscribeWithFilter in-process, or the
// kernel.subscribe RPC notification stream in daemon mode), and on each
// event re-derives the impacted set by re-running validate.diff over
// the current staged diff. There is no time.Tick / setInterval timer
// in this panel — quiescent workspaces produce no work.
//
// The panel is embeddable: Render returns a string the parent Model
// composes into its View output. SetSize hands the panel its render
// budget. Update is the bubbletea reducer the parent forwards relevant
// messages to.
type ImpactPanel struct {
	source ImpactDataSource

	// events is the subscription channel from which the panel reads
	// code.framework event-arrival notifications. The panel consumes
	// it via a tea.Cmd loop so the channel-receive is the only thing
	// that drives a re-derive — no polling.
	events <-chan ImpactEvent

	width  int
	height int

	state    ImpactState
	cursor   int // selected row in the findings column (for drill-down)
	expanded bool

	mu sync.Mutex // guards state mutations from the reducer test path
}

// ImpactState is the snapshot the panel renders. It is intentionally
// flat so tests can construct one inline and assert against the
// rendering directly.
type ImpactState struct {
	// TouchedFiles is the file-path list pulled from `git diff HEAD`.
	TouchedFiles []string
	// Touched is the projection of TouchedFiles through
	// Store.ListEntitiesByPath, deduped by entity id.
	Touched []ImpactEntityRef
	// Impacted is the per-kind grouping of dependents emitted by
	// stage-5 computeImpactedSet (Subscribers / Readers / Writers /
	// ContractTests / etc.).
	Impacted map[string][]change_process.DependentRef
	// Findings is the list of active missing_dependent_update
	// findings. Order matches Stage-6 deterministic emission.
	Findings []change_process.ValidationFinding
	// Err carries the last derive error (network / git / validate).
	// Non-fatal — the panel keeps the prior columns rendered.
	Err error
	// LastSeq is the seq of the last event that triggered a derive.
	// Surfaces in the panel footer so flakiness is visible.
	LastSeq uint64
}

// ImpactEntityRef is the panel's compact reference to a code.core
// entity. It does not depend on internal/code_core so the panel and
// its tests can be exercised without an in-process store.
type ImpactEntityRef struct {
	ID            string
	Kind          string
	QualifiedName string
	Path          string
}

// ImpactEvent is one push-channel delivery. The panel does not care
// about the full kernel.Event payload — just enough to log the seq +
// kind in the footer and to gate re-derive on the framework layer.
// Production wiring sources these from facts.EventLog.SubscribeWithFilter
// (in-process) or the kernel.subscribe JSON-RPC notification stream
// (daemon mode); tests construct them inline.
type ImpactEvent struct {
	Seq   uint64
	Layer string // expected "code.framework" — other layers are ignored by the reducer
	Kind  string // e.g. "RouteAdded", "EventPublisherAdded", "SchemaFieldChanged"
}

// ImpactDataSource is the read seam the panel uses to re-derive the
// touched + impacted set on every push event. Wired by the CLI
// startup: batch mode hands in a Store/EventLog-backed implementation;
// daemon mode hands in a Client-backed implementation that calls
// validate.diff over JSON-RPC. Tests pass a fake.
//
// The interface lives here (and not in svcapi) on purpose: it carries
// only what the panel needs and does NOT widen the long-lived
// jsonrpc.Consumer surface (per P2.T39 scope: "don't add new methods"
// to the typed client interface).
type ImpactDataSource interface {
	// StagedDiff returns the workspace's currently-staged unified diff
	// (the panel projects this through EntitiesForPath + ValidateDiff).
	// Returns nil + nil error when nothing is staged.
	StagedDiff(ctx context.Context) ([]byte, error)
	// EntitiesForPath maps one workspace-relative file path to the
	// code.core entities anchored there (Store.ListEntitiesByPath).
	EntitiesForPath(ctx context.Context, path string) ([]ImpactEntityRef, error)
	// ValidateDiff runs the 12-stage pipeline over `unified` and
	// returns the structured findings. The panel reads
	// missing_dependent_update findings + their FrameworkContext to
	// populate the Impacted and Findings columns.
	ValidateDiff(ctx context.Context, unified []byte) (*change_process.ValidateDiffResult, error)
}

// NewImpactPanel constructs the panel. Pass a nil events channel for a
// passive panel that only renders whatever state is pushed via
// ApplyEvent (useful for unit tests + the offline TUI).
func NewImpactPanel(source ImpactDataSource, events <-chan ImpactEvent) *ImpactPanel {
	return &ImpactPanel{
		source: source,
		events: events,
		state: ImpactState{
			Impacted: map[string][]change_process.DependentRef{},
		},
	}
}

// SetSize hands the panel its render budget.
func (p *ImpactPanel) SetSize(w, h int) {
	p.width = w
	p.height = h
}

// State returns a copy of the current state (used by tests).
func (p *ImpactPanel) State() ImpactState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// CursorRow returns the selected finding index, clamped to the
// renderable range. Used by tests + the drill-down view.
func (p *ImpactPanel) CursorRow() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cursor < 0 {
		return 0
	}
	if len(p.state.Findings) == 0 {
		return 0
	}
	if p.cursor >= len(p.state.Findings) {
		return len(p.state.Findings) - 1
	}
	return p.cursor
}

// IsExpanded reports whether the user has pressed Enter on the
// currently-selected finding (drill-down view).
func (p *ImpactPanel) IsExpanded() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.expanded
}

// --- bubbletea integration --------------------------------------------------

// impactEventMsg is the tea.Msg wrapper the panel uses internally to
// re-enter Update on each push delivery. impactDeriveMsg carries the
// result of a re-derive triggered by an event.
type impactEventMsg ImpactEvent

type impactDeriveMsg struct {
	state ImpactState
	err   error
}

// Init returns the initial tea.Cmd: a receiver loop on the events
// channel + an initial derive so the panel renders something even
// before the first event lands.
func (p *ImpactPanel) Init() tea.Cmd {
	cmds := []tea.Cmd{}
	if p.source != nil {
		cmds = append(cmds, p.deriveCmd(ImpactEvent{}))
	}
	if p.events != nil {
		cmds = append(cmds, p.recvCmd())
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// recvCmd is the only push-driven loop in the panel. It blocks on the
// events channel and re-emits the delivery as a tea.Msg; the bubbletea
// scheduler invokes the cmd again from Update after each delivery.
// No time.Tick. No setInterval. Quiescent workspaces produce zero
// recv-derive cycles.
func (p *ImpactPanel) recvCmd() tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-p.events
		if !ok {
			return nil
		}
		return impactEventMsg(ev)
	}
}

// deriveCmd runs StagedDiff → EntitiesForPath fan-out → ValidateDiff
// off the bubbletea goroutine. The result is delivered as an
// impactDeriveMsg which Update folds into state.
func (p *ImpactPanel) deriveCmd(ev ImpactEvent) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		next, err := p.derive(ctx, ev)
		return impactDeriveMsg{state: next, err: err}
	}
}

// Update is the panel's bubbletea reducer. It handles two messages:
// impactEventMsg (push delivery → schedule a derive + re-arm the recv
// loop), and impactDeriveMsg (derive result → fold into state). The
// parent Model forwards key events via the helper HandleKey path.
func (p *ImpactPanel) Update(msg tea.Msg) (*ImpactPanel, tea.Cmd) {
	switch m := msg.(type) {
	case impactEventMsg:
		// applyEvent is the pure reducer step that records the seq +
		// kind into the state's LastSeq footer immediately. The
		// follow-up deriveCmd refreshes the columns asynchronously.
		p.applyEvent(ImpactEvent(m))
		cmds := []tea.Cmd{p.recvCmd()}
		if p.source != nil && isFrameworkLayer(m.Layer) {
			cmds = append(cmds, p.deriveCmd(ImpactEvent(m)))
		}
		return p, tea.Batch(cmds...)
	case impactDeriveMsg:
		p.applyDerive(m)
		return p, nil
	}
	return p, nil
}

// HandleKey is called by the parent Model when the user presses a key
// while the panel is the active view. Returns true when the key was
// consumed (so the parent does not also act on it).
func (p *ImpactPanel) HandleKey(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch key {
	case "j", "down":
		if p.cursor < len(p.state.Findings)-1 {
			p.cursor++
		}
		return true
	case "k", "up":
		if p.cursor > 0 {
			p.cursor--
		}
		return true
	case "enter":
		if len(p.state.Findings) == 0 {
			return false
		}
		p.expanded = !p.expanded
		return true
	case "esc":
		if p.expanded {
			p.expanded = false
			return true
		}
	}
	return false
}

// applyEvent is the synchronous reducer step. Exported indirectly as
// ApplyEvent so tests can drive state transitions without spinning up a
// bubbletea Program. Pure: it only updates the LastSeq + Err footer
// and does NOT call the data source.
func (p *ImpactPanel) applyEvent(ev ImpactEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ev.Seq > p.state.LastSeq {
		p.state.LastSeq = ev.Seq
	}
}

// ApplyEvent is the test entry point — the bubbletea command path
// re-uses applyEvent under the same mutex. Returning a copy of the
// post-state keeps test asserts terse.
func (p *ImpactPanel) ApplyEvent(ev ImpactEvent) ImpactState {
	p.applyEvent(ev)
	return p.State()
}

// applyDerive merges a derive result into state. Non-fatal errors are
// preserved in state.Err so the prior columns stay rendered.
func (p *ImpactPanel) applyDerive(m impactDeriveMsg) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if m.err != nil {
		p.state.Err = m.err
		return
	}
	p.state = m.state
}

// ApplyDerive is the test entry point for folding a derive result.
func (p *ImpactPanel) ApplyDerive(state ImpactState, err error) ImpactState {
	p.applyDerive(impactDeriveMsg{state: state, err: err})
	return p.State()
}

// --- derivation --------------------------------------------------------------

// derive is the side-effecting step that re-computes the columns by
// re-projecting the staged diff. Called from deriveCmd off the bubbletea
// goroutine; tests exercise it via ApplyDerive with a hand-built state.
func (p *ImpactPanel) derive(ctx context.Context, ev ImpactEvent) (ImpactState, error) {
	next := ImpactState{
		Impacted: map[string][]change_process.DependentRef{},
		LastSeq:  ev.Seq,
	}
	if p.source == nil {
		return next, nil
	}
	diff, err := p.source.StagedDiff(ctx)
	if err != nil {
		return next, fmt.Errorf("staged diff: %w", err)
	}
	if len(diff) == 0 {
		// Empty diff → empty columns; this is a normal "clean
		// workspace" state, not an error.
		return next, nil
	}
	// Touched files: project the diff's `+++ b/<path>` lines to
	// workspace-relative paths. Reuses the change_process diff parser
	// indirectly via a lightweight local helper to avoid pulling the
	// entire pipeline package into the TUI.
	paths := touchedPathsFromDiff(diff)
	next.TouchedFiles = paths
	seen := map[string]struct{}{}
	for _, path := range paths {
		ents, perr := p.source.EntitiesForPath(ctx, path)
		if perr != nil {
			continue // best-effort: unmapped paths still show in TouchedFiles
		}
		for _, e := range ents {
			if _, dup := seen[e.ID]; dup {
				continue
			}
			seen[e.ID] = struct{}{}
			next.Touched = append(next.Touched, e)
		}
	}
	sort.SliceStable(next.Touched, func(i, j int) bool {
		if next.Touched[i].Kind != next.Touched[j].Kind {
			return next.Touched[i].Kind < next.Touched[j].Kind
		}
		return next.Touched[i].QualifiedName < next.Touched[j].QualifiedName
	})

	// Re-run validate.diff to harvest stage-6 findings + stage-7
	// FrameworkContext. ValidateDiff is bounded by the pipeline's
	// 12-stage walk; the impact panel runs at the daemon's cadence.
	res, verr := p.source.ValidateDiff(ctx, diff)
	if verr != nil {
		return next, fmt.Errorf("validate diff: %w", verr)
	}
	for _, f := range res.Findings {
		if f.Kind != change_process.FindingKindMissingDependentUpdate {
			continue
		}
		next.Findings = append(next.Findings, f)
		if f.FrameworkContext == nil {
			continue
		}
		for _, d := range f.FrameworkContext.Dependents {
			next.Impacted[d.Kind] = append(next.Impacted[d.Kind], d)
		}
	}
	// Dedupe + stable-sort each Impacted bucket so identical events
	// re-rendered after a re-derive produce identical strings (TUI
	// idempotence — matches the pipeline's idempotence contract).
	for k, ds := range next.Impacted {
		next.Impacted[k] = dedupeAndSortDependents(ds)
	}
	return next, nil
}

func dedupeAndSortDependents(ds []change_process.DependentRef) []change_process.DependentRef {
	seen := map[string]struct{}{}
	out := make([]change_process.DependentRef, 0, len(ds))
	for _, d := range ds {
		if _, dup := seen[d.ID]; dup {
			continue
		}
		seen[d.ID] = struct{}{}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].QualifiedName != out[j].QualifiedName {
			return out[i].QualifiedName < out[j].QualifiedName
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// touchedPathsFromDiff pulls `+++ b/<path>` and `--- a/<path>`
// (for deletions) markers out of a unified diff and returns the
// deduped workspace-relative path list.
func touchedPathsFromDiff(unified []byte) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, line := range strings.Split(string(unified), "\n") {
		var rest string
		switch {
		case strings.HasPrefix(line, "+++ "):
			rest = strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
		case strings.HasPrefix(line, "--- "):
			rest = strings.TrimSpace(strings.TrimPrefix(line, "--- "))
		default:
			continue
		}
		rest = strings.TrimPrefix(rest, "a/")
		rest = strings.TrimPrefix(rest, "b/")
		if rest == "" || rest == "/dev/null" {
			continue
		}
		if _, dup := seen[rest]; dup {
			continue
		}
		seen[rest] = struct{}{}
		out = append(out, rest)
	}
	sort.Strings(out)
	return out
}

// isFrameworkLayer reports whether the event layer warrants a derive.
// We treat the empty string (initial derive at panel start) as a match
// so the first render is populated even without an event in flight.
func isFrameworkLayer(layer string) bool {
	return layer == "" || layer == "code.framework"
}

// --- git-backed default ImpactDataSource ------------------------------------

// GitDiffStagedDiff is the helper the CLI uses to wire a default
// StagedDiff implementation: shells out to `git diff HEAD` in `root`.
// Exported so external surfaces (cli/tui.go, future studio) can reuse
// it without re-implementing the exec dance.
func GitDiffStagedDiff(ctx context.Context, root string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff HEAD in %s: %w", root, err)
	}
	return out, nil
}

// --- rendering --------------------------------------------------------------

var (
	impactColTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#5fafff"))
	impactKindTag  = lipgloss.NewStyle().Foreground(lipgloss.Color("#d29922"))
	impactMuted    = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	impactSevHigh  = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5f5f")).Bold(true)
)

// Render returns the panel's three-column view. Layout is a vertical
// stack of the three columns (Touched / Impacted / Findings); a full
// horizontal layout requires the parent to grant >= 90 cols of width
// — narrower terminals fall back to the stacked form.
func (p *ImpactPanel) Render() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.expanded && len(p.state.Findings) > 0 {
		return p.renderDrilldownLocked()
	}
	var b strings.Builder
	b.WriteString(impactColTitle.Render("Touched"))
	b.WriteString("\n")
	if len(p.state.Touched) == 0 && len(p.state.TouchedFiles) == 0 {
		b.WriteString(impactMuted.Render("  (no staged diff)"))
		b.WriteString("\n")
	} else {
		for _, e := range p.state.Touched {
			fmt.Fprintf(&b, "  %s %s\n", impactKindTag.Render(e.Kind), e.QualifiedName)
		}
		// File-only rows (no entity match yet).
		entityPaths := map[string]struct{}{}
		for _, e := range p.state.Touched {
			entityPaths[e.Path] = struct{}{}
		}
		for _, f := range p.state.TouchedFiles {
			if _, mapped := entityPaths[f]; mapped {
				continue
			}
			fmt.Fprintf(&b, "  %s %s\n", impactMuted.Render("file"), f)
		}
	}
	b.WriteString("\n")
	b.WriteString(impactColTitle.Render("Impacted"))
	b.WriteString("\n")
	if len(p.state.Impacted) == 0 {
		b.WriteString(impactMuted.Render("  (none)"))
		b.WriteString("\n")
	} else {
		kinds := make([]string, 0, len(p.state.Impacted))
		for k := range p.state.Impacted {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			ds := p.state.Impacted[k]
			fmt.Fprintf(&b, "  %s (%d)\n", impactKindTag.Render(k), len(ds))
			for _, d := range ds {
				fmt.Fprintf(&b, "    - %s\n", d.QualifiedName)
			}
		}
	}
	b.WriteString("\n")
	b.WriteString(impactColTitle.Render("Findings"))
	b.WriteString("\n")
	if len(p.state.Findings) == 0 {
		b.WriteString(impactMuted.Render("  (no missing_dependent_update findings)"))
		b.WriteString("\n")
	} else {
		for i, f := range p.state.Findings {
			marker := "  "
			if i == p.cursor {
				marker = "> "
			}
			fmt.Fprintf(&b, "%s%s %s — %s\n", marker, severityDot(f.Severity), f.ID, f.Subject.Qualified)
		}
		b.WriteString(impactMuted.Render("  [enter] inspect  [j/k] move"))
		b.WriteString("\n")
	}
	if p.state.Err != nil {
		fmt.Fprintf(&b, "\n%s\n", impactSevHigh.Render("error: "+p.state.Err.Error()))
	}
	fmt.Fprintf(&b, "\n%s\n", impactMuted.Render(fmt.Sprintf("(last event seq=%d, push-driven — no polling)", p.state.LastSeq)))
	return b.String()
}

func (p *ImpactPanel) renderDrilldownLocked() string {
	idx := p.cursor
	if idx < 0 || idx >= len(p.state.Findings) {
		idx = 0
	}
	f := p.state.Findings[idx]
	var b strings.Builder
	b.WriteString(impactColTitle.Render("Finding — drilldown"))
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "  ID:        %s\n", f.ID)
	fmt.Fprintf(&b, "  Kind:      %s\n", f.Kind)
	fmt.Fprintf(&b, "  Severity:  %s\n", f.Severity)
	fmt.Fprintf(&b, "  Subject:   %s (%s)\n", f.Subject.Qualified, f.Subject.EntityKind)
	if f.FrameworkContext != nil {
		fmt.Fprintf(&b, "\n  %s %s (%s)\n", impactKindTag.Render("TouchedSubject:"),
			f.FrameworkContext.TouchedSubject.QualifiedName,
			f.FrameworkContext.TouchedKind)
		b.WriteString("\n  Dependents:\n")
		for _, d := range f.FrameworkContext.Dependents {
			fmt.Fprintf(&b, "    - %s %s — %s\n",
				impactKindTag.Render(d.Kind), d.QualifiedName, d.Reason)
		}
	}
	if len(f.Evidence) > 0 {
		b.WriteString("\n  Evidence:\n")
		for _, e := range f.Evidence {
			fmt.Fprintf(&b, "    - %s: %s\n", e.Kind, e.Detail)
		}
	}
	b.WriteString("\n")
	b.WriteString(impactMuted.Render("  [esc] back  [enter] collapse"))
	return b.String()
}

func severityDot(sev string) string {
	switch sev {
	case "critical", "high":
		return impactSevHigh.Render("●")
	case "medium":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#d29922")).Render("●")
	case "low", "info":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#1f883d")).Render("●")
	}
	return impactMuted.Render("●")
}
