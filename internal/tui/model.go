package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/shivamstaq/graph-harness/internal/change_process"
	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
)

// Model is the bubbletea model for the cockpit.
//
// Per F2 / plan/answers/04 §5, the TUI consumes the daemon via the
// JSON-RPC Consumer interface — concrete shape may be either the
// in-process *jsonrpc.Service (when no daemon is running) or a
// *jsonrpc.ClientService wrapping a connected client (when the daemon
// owns the workspace). Both satisfy the same surface; the cockpit
// doesn't care which.
type Model struct {
	svc jsonrpc.Consumer

	view   int // 0 status, 1 findings, 2 conflicts, 3 doctor
	width  int
	height int
	status jsonrpc.StatusResult
	finds  []change_process.ValidationFinding
	confs  []jsonrpc.ConflictRecord
	doctor jsonrpc.DoctorReportResult
	cursor int
	err    error
	tick   time.Time
}

// NewModel constructs the model bound to the given Consumer. svc may
// be nil for an offline/dry-run TUI invocation that just renders the
// chrome and exits — useful in CI specs.
func NewModel(svc jsonrpc.Consumer) *Model {
	return &Model{svc: svc, tick: time.Now()}
}

// SetFindings hydrates the findings view (caller-driven; the TUI can be
// launched after a validate-diff invocation that produced findings).
func (m *Model) SetFindings(f []change_process.ValidationFinding) { m.finds = f }

// Run runs the bubbletea program in alternate-screen mode. Blocks until the
// user exits. Caller passes ctx for cancellation.
func (m *Model) Run(_ context.Context) error {
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.refreshCmd(), tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }))
}

type tickMsg time.Time

type refreshMsg struct {
	status jsonrpc.StatusResult
	confs  []jsonrpc.ConflictRecord
	doctor jsonrpc.DoctorReportResult
	err    error
}

func (m *Model) refreshCmd() tea.Cmd {
	return func() tea.Msg {
		if m.svc == nil {
			return refreshMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		st, err := m.svc.Status(ctx)
		if err != nil {
			return refreshMsg{err: err}
		}
		c, _ := m.svc.ConflictsList(ctx)
		// Doctor probe is bounded; failures fall through quietly so the
		// cockpit still renders the other panes.
		doctor, _ := m.svc.DoctorReport(ctx)
		return refreshMsg{status: st, confs: c.Conflicts, doctor: doctor}
	}
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "tab", "right", "l":
			m.view = (m.view + 1) % 4
			m.cursor = 0
		case "shift+tab", "left", "h":
			m.view = (m.view + 3) % 4
			m.cursor = 0
		case "down", "j":
			m.cursor++
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "r":
			return m, m.refreshCmd()
		}
	case tickMsg:
		m.tick = time.Time(msg)
		return m, tea.Batch(m.refreshCmd(), tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }))
	case refreshMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.status = msg.status
			m.confs = msg.confs
			m.doctor = msg.doctor
			m.err = nil
		}
	}
	return m, nil
}

// --- styles ----------------------------------------------------------------

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#5fafff"))
	mutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	tabActive  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffffff")).Background(lipgloss.Color("#1f6feb")).Padding(0, 1)
	tabIdle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Padding(0, 1)
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5f5f")).Bold(true)
)

// View implements tea.Model.
func (m *Model) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("graph-harness"))
	b.WriteString(mutedStyle.Render("  cockpit"))
	b.WriteString("\n")

	tabs := []string{"workspace", "findings", "conflicts", "doctor"}
	for i, t := range tabs {
		if i == m.view {
			b.WriteString(tabActive.Render(t))
		} else {
			b.WriteString(tabIdle.Render(t))
		}
	}
	b.WriteString("\n\n")

	switch m.view {
	case 0:
		b.WriteString(m.renderStatus())
	case 1:
		b.WriteString(m.renderFindings())
	case 2:
		b.WriteString(m.renderConflicts())
	case 3:
		b.WriteString(m.renderDoctor())
	}

	if m.err != nil {
		b.WriteString("\n")
		b.WriteString(errStyle.Render(fmt.Sprintf("error: %v", m.err)))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("[tab] switch view  [j/k] move  [r] refresh  [q] quit  (workspace · findings · conflicts · doctor)"))
	return b.String()
}

func (m *Model) renderStatus() string {
	if m.svc == nil {
		return mutedStyle.Render("(no service attached — TUI launched offline)")
	}
	rows := [][2]string{
		{"Workspace", m.status.WorkspaceRoot},
		{"Workspace ID", m.status.WorkspaceID},
		{"Socket", m.status.SocketPath},
		{"Event log", m.status.EventLogPath},
		{"Last seq", fmt.Sprintf("%d", m.status.LastSeq)},
		{"Overlay decls", fmt.Sprintf("%d", m.status.OverlayCount)},
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(titleStyle.Render(r[0]))
		b.WriteString("  ")
		b.WriteString(r[1])
		b.WriteString("\n")
	}
	return b.String()
}

func (m *Model) renderFindings() string {
	if len(m.finds) == 0 {
		return mutedStyle.Render("no findings to show. run `graph-harness validate-diff` then re-launch the TUI to hydrate.")
	}
	var b strings.Builder
	for i, f := range m.finds {
		marker := "  "
		if i == m.cursor {
			marker = "▸ "
		}
		fmt.Fprintf(&b, "%s%s %s — %s (%s)\n", marker, sevTag(f.Severity), f.ID, f.Kind, f.Subject.Qualified)
		if f.Subject.Flow != "" {
			fmt.Fprintf(&b, "    flow: %s\n", f.Subject.Flow)
		}
		for _, e := range f.Evidence {
			fmt.Fprintf(&b, "    - %s: %s\n", e.Kind, e.Detail)
		}
	}
	return b.String()
}

func (m *Model) renderConflicts() string {
	if len(m.confs) == 0 {
		return mutedStyle.Render("no SymbolDisambiguation events recorded yet (Phase 1 unifier feeds this view).")
	}
	var b strings.Builder
	for i, c := range m.confs {
		marker := "  "
		if i == m.cursor {
			marker = "▸ "
		}
		fmt.Fprintf(&b, "%sseq=%d %s\n", marker, c.Seq, c.Qualified)
		fmt.Fprintf(&b, "    selector: %s    sources: %s\n", c.Selector, strings.Join(c.Sources, ", "))
		fmt.Fprintf(&b, "    detected: %s\n", c.DetectedAt.Format(time.RFC3339))
	}
	return b.String()
}

func (m *Model) renderDoctor() string {
	if len(m.doctor.Languages) == 0 {
		return mutedStyle.Render("doctor: no detection report yet (wait for refresh, or run `graph-harness doctor`).")
	}
	var b strings.Builder
	for _, lang := range m.doctor.Languages {
		b.WriteString(titleStyle.Render(lang.LanguageID))
		b.WriteString("\n")
		for _, t := range lang.Tools {
			glyph := "?"
			color := mutedStyle
			switch t.Status {
			case "available":
				glyph = "✓"
				color = lipgloss.NewStyle().Foreground(lipgloss.Color("#1f883d"))
			case "embedded":
				glyph = "✓ embed"
				color = mutedStyle
			case "missing":
				glyph = "✗"
				color = errStyle
			}
			line := fmt.Sprintf("  %s %s", glyph, t.Name)
			if t.Path != "" {
				line += "  " + mutedStyle.Render(t.Path)
			} else if len(t.InstallHints) > 0 {
				line += "  " + mutedStyle.Render("install: "+t.InstallHints[0].Command)
			}
			b.WriteString(color.Render(line))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func sevTag(sev string) string {
	switch sev {
	case "critical", "high":
		return errStyle.Render("●")
	case "medium":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#d29922")).Render("●")
	case "low":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#1f883d")).Render("●")
	}
	return mutedStyle.Render("●")
}
