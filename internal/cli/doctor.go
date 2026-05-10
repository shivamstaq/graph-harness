package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/shivamstaq/graph-harness/internal/source_live"
	"github.com/shivamstaq/graph-harness/internal/source_live/detect"
)

// newDoctorCmd is the user-facing surface for graph-harness's
// detection report. JSON-first canonical (per Q6 of the design
// interview): the underlying detect.Report slice is the single source
// of truth; --json emits a slim, pretty-printed view, --json --verbose
// emits the full envelope (probe chain + source provenance).
//
// Flags:
//
//	--json                 emit machine-readable doctor envelope
//	--verbose              expand text view with the probe trace; expand
//	                       --json with per-tool source / probe_chain
//	--print-install        emit shell install commands for missing tools
//	--print-index-recipe   emit SCIP-generation commands per language
//	--language=<id>        narrow probing to one language id
//	--strict               exit code 3 if any in-workspace primary
//	                        extractor is missing (CI mode)
//
// SPEC §6.18 ("never supplier"): the command never executes installs.
// --print-install outputs a snippet the user pipes to bash themselves.
func newDoctorCmd() *cobra.Command {
	var (
		jsonOut          bool
		verbose          bool
		printInstall     bool
		printIndexRecipe bool
		language         string
		strict           bool
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Probe the environment for LSP/SCIP/parser tooling and report what's available",
		Long: `Probe the workspace and the user's $PATH / package-manager dirs for
every tool graph-harness needs (gopls, scip-go, typescript-language-server,
scip-typescript, pyright, scip-python, …). Reports what's installed, where
it was found, and — for missing tools — the project-aware install command.

graph-harness never auto-installs (SPEC §6.18); this command surfaces what
your environment looks like and what to run yourself. Use --print-install
to copy-paste a shell snippet, --verbose to see the probe trace.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			workspaceRoot := workspaceRootForDoctor()
			reports := runDoctorProbe(cmd.Context(), workspaceRoot, language)
			out := cmd.OutOrStdout()
			switch {
			case jsonOut:
				return writeDoctorJSON(out, workspaceRoot, reports, verbose)
			case printInstall:
				return writeDoctorInstall(out, reports)
			case printIndexRecipe:
				return writeDoctorIndexRecipe(out, reports)
			default:
				if err := renderDoctor(out, workspaceRoot, reports, verbose); err != nil {
					return err
				}
			}
			if strict {
				if missing := missingPrimaryExtractors(workspaceRoot, reports); missing > 0 {
					// Also goes to stdout so e2e specs and shell pipelines
					// can grep the warning without a separate stderr capture.
					_, _ = fmt.Fprintf(out,
						"\nstrict: %d primary extractor(s) missing for in-workspace languages\n", missing)
					os.Exit(3)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable doctor envelope")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "expand output: probe trace in text mode; per-tool source + probe_chain in --json")
	cmd.Flags().BoolVar(&printInstall, "print-install", false, "emit a shell snippet of install commands for missing tools")
	cmd.Flags().BoolVar(&printIndexRecipe, "print-index-recipe", false, "emit SCIP-generation commands per language (replaces deferred `graph-harness index`)")
	cmd.Flags().StringVar(&language, "language", "", "narrow probing to one language id (go, typescript, python)")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit 3 when any primary extractor is missing for an in-workspace language")
	return cmd
}

// workspaceRootForDoctor returns the workspace root for detection. If
// the user is inside an initialized graph-harness workspace we use
// that; otherwise we fall back to cwd so the command works without an
// init step (useful in agent-context probes).
func workspaceRootForDoctor() string {
	if ws, err := activeWorkspace(); err == nil && ws != nil {
		return ws.Root
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
}

// runDoctorProbe runs the detector registry, optionally narrowed to a
// single language id. Errors during probing are surfaced as
// status=missing entries (consistent with the "data, not crash"
// contract).
func runDoctorProbe(ctx context.Context, root, language string) []detect.Report {
	if ctx == nil {
		ctx = context.Background()
	}
	// Bound the whole sweep — package-manager queries can be slow on
	// cold caches.
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	registry := detect.NewRegistry()
	languages := registry.Languages()
	if language != "" {
		languages = registry.FilterLanguages([]string{language})
	}
	reports := make([]detect.Report, 0, len(languages))
	for _, lang := range languages {
		d := registry.LookUp(lang)
		if d == nil {
			continue
		}
		r, err := d.Probe(ctx, root)
		if err != nil {
			reports = append(reports, detect.Report{
				LanguageID: lang,
				Tools: []detect.ToolReport{{
					Name:   "<probe-error>",
					Class:  detect.ToolClassLSP,
					Status: detect.StatusMissing,
					ProbeChain: []detect.ProbeStep{
						{Location: "(detector)", Found: false, Reason: err.Error()},
					},
				}},
			})
			continue
		}
		reports = append(reports, r)
	}
	return reports
}

// doctorEnvelope is the wire shape for `doctor --json`. The same struct
// serves both the slim default and the verbose form: verbose populates
// Source / ServerID / ProbeChain on each tool entry; default omits.
type doctorEnvelope struct {
	WorkspaceRoot string            `json:"workspace_root"`
	GeneratedAt   time.Time         `json:"generated_at"`
	Languages     []string          `json:"languages"`
	Summary       doctorSummary     `json:"summary"`
	Coverage      []coverageEntry   `json:"coverage"`
	Tools         []doctorToolEntry `json:"tools"`
}

type doctorSummary struct {
	Total     int `json:"total"`
	Available int `json:"available"`
	Missing   int `json:"missing"`
}

// coverageEntry is one row of the per-language coverage banner / JSON
// coverage[] array. Status is one of "ok" | "partial" | "none".
type coverageEntry struct {
	Language string `json:"language"`
	Status   string `json:"status"`
	Note     string `json:"note,omitempty"`
}

// doctorToolEntry flattens (language, tool) pairs into one row. The
// verbose-only fields ride along under omitempty so the shape stays
// stable.
type doctorToolEntry struct {
	Language string             `json:"language"`
	Name     string             `json:"name"`
	Class    detect.ToolClass   `json:"class"`
	Status   detect.ToolStatus  `json:"status"`
	Version  string             `json:"version,omitempty"`
	Path     string             `json:"path,omitempty"`
	Install  string             `json:"install,omitempty"`
	Source   detect.ToolSource  `json:"source,omitempty"`
	ServerID string             `json:"server_id,omitempty"`
	Probe    []detect.ProbeStep `json:"probe_chain,omitempty"`
}

// writeDoctorJSON emits the doctor envelope. Pretty-printed in both
// modes; verbose adds source + probe_chain to each tool entry.
func writeDoctorJSON(w io.Writer, workspaceRoot string, reports []detect.Report, verbose bool) error {
	env := buildDoctorEnvelope(workspaceRoot, reports, verbose)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

// buildDoctorEnvelope assembles the JSON wire payload. Pure function —
// no I/O, no timestamps beyond detect.Now.
func buildDoctorEnvelope(workspaceRoot string, reports []detect.Report, verbose bool) doctorEnvelope {
	var tools []doctorToolEntry
	var available, missing int
	for _, r := range reports {
		for _, t := range r.Tools {
			entry := doctorToolEntry{
				Language: r.LanguageID,
				Name:     t.Name,
				Class:    t.Class,
				Status:   t.Status,
				Version:  t.Version,
				Path:     t.Path,
			}
			if t.Status == detect.StatusMissing {
				entry.Install = preferredHint(t.InstallHints)
			}
			if verbose {
				entry.Source = t.Source
				entry.ServerID = t.ServerID
				entry.Probe = t.ProbeChain
			}
			tools = append(tools, entry)
			switch t.Status {
			case detect.StatusMissing:
				missing++
			case detect.StatusAvailable, detect.StatusEmbedded, detect.StatusVersionMismatch:
				available++
			}
		}
	}
	return doctorEnvelope{
		WorkspaceRoot: workspaceRoot,
		GeneratedAt:   detect.Now().UTC(),
		Languages:     languagesFromReports(reports),
		Summary: doctorSummary{
			Total:     available + missing,
			Available: available,
			Missing:   missing,
		},
		Coverage: coverageEntries(workspaceRoot, reports),
		Tools:    tools,
	}
}

// writeDoctorInstall emits a shell snippet (one command per line) the
// user can pipe to bash. Honors per-tool preferred manager.
func writeDoctorInstall(w io.Writer, reports []detect.Report) error {
	fmt.Fprintln(w, "#!/usr/bin/env bash")
	fmt.Fprintln(w, "# graph-harness doctor --print-install")
	fmt.Fprintln(w, "# Run these to install missing extractors. graph-harness itself")
	fmt.Fprintln(w, "# never auto-installs (SPEC §6.18); you are in control.")
	fmt.Fprintln(w, "set -e")
	fmt.Fprintln(w)
	any := false
	for _, r := range reports {
		for _, t := range r.Tools {
			if t.Status != detect.StatusMissing {
				continue
			}
			cmd := preferredHint(t.InstallHints)
			if cmd == "" {
				continue
			}
			fmt.Fprintf(w, "# %s/%s\n", r.LanguageID, t.Name)
			fmt.Fprintln(w, cmd)
			fmt.Fprintln(w)
			any = true
		}
	}
	if !any {
		fmt.Fprintln(w, "# (no missing extractors)")
	}
	return nil
}

// writeDoctorIndexRecipe emits SCIP generation commands for each
// language whose scip-* indexer is the relevant tool. The recipe
// assumes the indexer is on PATH; for missing indexers it points at
// the install hint instead.
func writeDoctorIndexRecipe(w io.Writer, reports []detect.Report) error {
	fmt.Fprintln(w, "#!/usr/bin/env bash")
	fmt.Fprintln(w, "# graph-harness doctor --print-index-recipe")
	fmt.Fprintln(w, "# Generate SCIP indexes for every language in this workspace.")
	fmt.Fprintln(w, "# graph-harness will pick them up via the .scip-index/ watcher.")
	fmt.Fprintln(w, "set -e")
	fmt.Fprintln(w, "mkdir -p .scip-index")
	fmt.Fprintln(w)
	for _, r := range reports {
		for _, t := range r.Tools {
			if t.Class != detect.ToolClassSCIP {
				continue
			}
			if t.Status == detect.StatusMissing {
				fmt.Fprintf(w, "# %s/%s — not installed; run first:\n", r.LanguageID, t.Name)
				if cmd := preferredHint(t.InstallHints); cmd != "" {
					fmt.Fprintf(w, "# %s\n", cmd)
				}
				fmt.Fprintln(w)
				continue
			}
			fmt.Fprintf(w, "# %s/%s\n", r.LanguageID, t.Name)
			switch r.LanguageID {
			case "go":
				fmt.Fprintf(w, "%s --module-root=. --output=.scip-index/index.go.scip\n", t.Path)
			case "typescript":
				fmt.Fprintf(w, "%s index --infer-tsconfig --output=.scip-index/index.ts.scip\n", t.Path)
			case "python":
				fmt.Fprintf(w, "%s index --output=.scip-index/index.py.scip .\n", t.Path)
			default:
				fmt.Fprintf(w, "# (no recipe template for %s; consult upstream docs)\n", r.LanguageID)
			}
			fmt.Fprintln(w)
		}
	}
	return nil
}

// preferredHint picks the install hint marked Preferred=true, falling
// back to the first hint in the list.
func preferredHint(hints []detect.InstallHint) string {
	for _, h := range hints {
		if h.Preferred {
			return h.Command
		}
	}
	if len(hints) > 0 {
		return hints[0].Command
	}
	return ""
}

// coverageEntries returns one entry per workspace language describing
// how that language will actually be served. Status:
//
//   - "ok"      — LSP + SCIP + parser all available (full three-source agreement)
//   - "partial" — LSP-only, SCIP-only, or parser-only
//   - "none"    — nothing usable for that language
//
// Languages that aren't present in the workspace are skipped — we only
// surface coverage for code the user actually has.
func coverageEntries(workspaceRoot string, reports []detect.Report) []coverageEntry {
	wsLangs := workspaceLanguages(workspaceRoot)
	if len(wsLangs) == 0 {
		return nil
	}
	out := make([]coverageEntry, 0, len(wsLangs))
	for _, r := range reports {
		if !wsLangs[r.LanguageID] {
			continue
		}
		hasLSP, hasSCIP, hasParser := false, false, false
		for _, t := range r.Tools {
			switch t.Class {
			case detect.ToolClassLSP:
				if t.Status == detect.StatusAvailable {
					hasLSP = true
				}
			case detect.ToolClassSCIP:
				if t.Status == detect.StatusAvailable {
					hasSCIP = true
				}
			case detect.ToolClassParser:
				if t.Status == detect.StatusEmbedded || t.Status == detect.StatusAvailable {
					hasParser = true
				}
			}
		}
		entry := coverageEntry{Language: r.LanguageID}
		switch {
		case hasLSP && hasSCIP && hasParser:
			entry.Status = "ok"
		case !hasLSP && hasSCIP && hasParser:
			entry.Status = "partial"
			entry.Note = "no live LSP; index_scip + tree-sitter only"
		case hasLSP && !hasSCIP && hasParser:
			entry.Status = "partial"
			entry.Note = "live LSP + tree-sitter only (no SCIP indexer)"
		case !hasLSP && !hasSCIP && hasParser:
			entry.Status = "partial"
			entry.Note = "tree-sitter only (low confidence)"
		default:
			entry.Status = "none"
			entry.Note = "no extractor available"
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Language < out[j].Language })
	return out
}

// languagesFromReports returns the set of language ids in the report
// list, sorted, for the text view's header and the JSON envelope.
func languagesFromReports(reports []detect.Report) []string {
	seen := map[string]bool{}
	for _, r := range reports {
		seen[r.LanguageID] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// missingPrimaryExtractors counts missing LSP + SCIP entries for
// languages actually present in the workspace. Used by --strict.
func missingPrimaryExtractors(workspaceRoot string, reports []detect.Report) int {
	wsLangs := workspaceLanguages(workspaceRoot)
	missing := 0
	for _, r := range reports {
		if len(wsLangs) > 0 && !wsLangs[r.LanguageID] {
			continue
		}
		for _, t := range r.Tools {
			if (t.Class == detect.ToolClassLSP || t.Class == detect.ToolClassSCIP) && t.Status == detect.StatusMissing {
				missing++
			}
		}
	}
	return missing
}

// workspaceLanguages returns the set of languages detected in
// workspaceRoot by walking the source tree. Returns an empty set when
// root is empty or unreadable. Mirrors the orchestrator's
// SourceFilesUnder file-extension routing so doctor's coverage summary
// matches what the indexer will actually see.
func workspaceLanguages(root string) map[string]bool {
	if root == "" {
		return nil
	}
	out := map[string]bool{}
	files, err := walkWorkspaceFiles(root)
	if err != nil {
		return out
	}
	for _, f := range files {
		if lang := source_live.LanguageOf(f); lang != "" {
			out[lang] = true
		}
		if len(out) >= 8 { // sane cap; once we've seen Go/TS/Py/etc we know.
			break
		}
	}
	return out
}

// walkWorkspaceFiles is a thin helper that mirrors the orchestrator's
// file-walk pruning. We don't share extract.SourceFilesUnder directly
// because the cli package can't import extract without a cycle (cli →
// extract → … → daemon → cli through the workspace helpers).
func walkWorkspaceFiles(root string) ([]string, error) {
	var out []string
	if _, err := os.ReadDir(root); err != nil {
		return nil, err
	}
	stack := []string{root}
	for len(stack) > 0 {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		es, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range es {
			name := e.Name()
			if e.IsDir() {
				if isSkippableDir(name) {
					continue
				}
				stack = append(stack, dir+"/"+name)
				continue
			}
			out = append(out, dir+"/"+name)
		}
		if len(out) > 5000 { // bound; we just need to know what languages exist.
			break
		}
	}
	return out, nil
}

func isSkippableDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "dist", "build", "bin",
		"venv", ".venv", "__pycache__", "target", ".graph-harness",
		".scip-index":
		return true
	}
	return strings.HasPrefix(name, ".")
}

// ---------- Text rendering ----------

// renderDoctor renders the per-language card view. When verbose, the
// probe trace is printed first so the card view stays at the bottom of
// the screen where users expect the answer.
func renderDoctor(w io.Writer, workspaceRoot string, reports []detect.Report, verbose bool) error {
	st := newStyle(w)
	if verbose {
		renderProbeTrace(w, st, reports)
		fmt.Fprintln(w)
	}

	if workspaceRoot != "" {
		fmt.Fprintf(w, "Workspace: %s\n", workspaceRoot)
	}
	if langs := languagesFromReports(reports); len(langs) > 0 {
		fmt.Fprintf(w, "Languages: %s\n", strings.Join(langs, ", "))
	}
	fmt.Fprintln(w)

	renderCoverage(w, st, workspaceRoot, reports)
	for _, r := range reports {
		renderLanguageCard(w, st, r)
	}
	renderLegend(w, st)
	renderSummaryAndNextSteps(w, st, reports)
	return nil
}

func renderCoverage(w io.Writer, st *style, workspaceRoot string, reports []detect.Report) {
	entries := coverageEntries(workspaceRoot, reports)
	st.section(w, "Coverage")
	if len(entries) == 0 {
		fmt.Fprintln(w, "  (no source files detected — coverage skipped)")
		fmt.Fprintln(w)
		return
	}
	allOK := true
	for _, e := range entries {
		if e.Status != "ok" {
			allOK = false
			break
		}
	}
	if allOK {
		fmt.Fprintf(w, "  %s All extractors available for in-workspace languages.\n",
			st.render(st.ok, "✓"))
		fmt.Fprintln(w)
		return
	}
	langW := 0
	for _, e := range entries {
		if len(e.Language) > langW {
			langW = len(e.Language)
		}
	}
	for _, e := range entries {
		switch e.Status {
		case "ok":
			fmt.Fprintf(w, "  %s  %-*s  %s\n",
				st.render(st.ok, "✓"), langW, e.Language,
				st.render(st.dim, "fully covered"))
		case "partial":
			fmt.Fprintf(w, "  %s  %-*s  %s\n",
				st.render(st.warn, "⚠"), langW, e.Language, e.Note)
		case "none":
			fmt.Fprintf(w, "  %s  %-*s  %s\n",
				st.render(st.miss, "✗"), langW, e.Language, e.Note)
		}
	}
	fmt.Fprintln(w)
}

func renderLanguageCard(w io.Writer, st *style, r detect.Report) {
	st.section(w, r.LanguageID)
	// Compute name column width across this card so versions/statuses
	// align within the card without spilling between cards.
	nameW := 0
	for _, t := range r.Tools {
		if len(t.Name) > nameW {
			nameW = len(t.Name)
		}
	}
	for _, t := range r.Tools {
		renderToolRow(w, st, t, nameW)
	}
	fmt.Fprintln(w)
}

func renderToolRow(w io.Writer, st *style, t detect.ToolReport, nameW int) {
	var glyph, statusOrVersion string
	switch t.Status {
	case detect.StatusAvailable:
		glyph = st.render(st.ok, "✓")
		if t.Version != "" {
			statusOrVersion = st.render(st.dim, t.Version)
		} else {
			statusOrVersion = st.render(st.dim, "available")
		}
	case detect.StatusVersionMismatch:
		glyph = st.render(st.warn, "≠")
		ver := t.Version
		if ver == "" {
			ver = "version mismatch"
		}
		statusOrVersion = st.render(st.warn, ver)
	case detect.StatusMissing:
		glyph = st.render(st.miss, "✗")
		statusOrVersion = st.render(st.miss, "missing")
	case detect.StatusEmbedded:
		glyph = st.render(st.dim, "•")
		statusOrVersion = st.render(st.dim, "bundled")
	default:
		glyph = "?"
		statusOrVersion = string(t.Status)
	}
	fmt.Fprintf(w, "  %s  %-*s  %s\n",
		glyph, nameW, st.render(st.bold, t.Name), statusOrVersion)

	// Continuation line: full path or full install command. No truncation.
	cont := toolContinuation(t)
	if cont != "" {
		fmt.Fprintf(w, "       %s\n", st.render(st.dim, cont))
	}
}

// toolContinuation returns the line that goes under the tool name.
// Available tools get a "<path> (<source>)" line; missing tools get the
// preferred install command; embedded tools get nothing (the bundled
// status is self-explanatory and the legend covers it).
func toolContinuation(t detect.ToolReport) string {
	switch t.Status {
	case detect.StatusAvailable, detect.StatusVersionMismatch:
		if t.Path == "" {
			return ""
		}
		src := sourceLabel(t.Source)
		if src == "" {
			return homeShort(t.Path)
		}
		return fmt.Sprintf("%s  (%s)", homeShort(t.Path), src)
	case detect.StatusMissing:
		return preferredHint(t.InstallHints)
	}
	return ""
}

// sourceLabel renders the ToolSource enum as a short, human-readable
// label for the path-continuation line.
func sourceLabel(s detect.ToolSource) string {
	switch s {
	case detect.SourceProjectLocal:
		return "project-local"
	case detect.SourceEcosystemUser:
		return "ecosystem-user"
	case detect.SourcePATH:
		return "PATH"
	case detect.SourceEmbedded:
		return "embedded"
	}
	return ""
}

// homeShort replaces a $HOME prefix with "~" — readability only, no
// truncation. The result still copy-pastes to a real path because
// shells expand "~" the same way.
func homeShort(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

func renderLegend(w io.Writer, st *style) {
	st.section(w, "Legend")
	fmt.Fprintf(w, "  %s  available on PATH or in a package-manager directory\n", st.render(st.ok, "✓"))
	fmt.Fprintf(w, "  %s  not installed — run with --print-install for a copy-paste shell snippet\n", st.render(st.miss, "✗"))
	fmt.Fprintf(w, "  %s  bundled — parser compiled into the graph-harness binary\n", st.render(st.dim, "•"))
	fmt.Fprintln(w)
}

func renderSummaryAndNextSteps(w io.Writer, st *style, reports []detect.Report) {
	missing, total := 0, 0
	for _, r := range reports {
		for _, t := range r.Tools {
			total++
			if t.Status == detect.StatusMissing {
				missing++
			}
		}
	}
	if missing == 0 {
		fmt.Fprintf(w, "All %d extractors present.\n", total)
	} else {
		fmt.Fprintf(w, "%d of %d extractors missing.\n", missing, total)
	}
	fmt.Fprintln(w)
	st.section(w, "Next steps")
	fmt.Fprintf(w, "  • %s   shell snippet to install missing tools\n",
		st.render(st.bold, "graph-harness doctor --print-install"))
	fmt.Fprintf(w, "  • %s            machine-readable report\n",
		st.render(st.bold, "graph-harness doctor --json"))
	fmt.Fprintf(w, "  • %s         probe trace for each tool\n",
		st.render(st.bold, "graph-harness doctor --verbose"))
}

func renderProbeTrace(w io.Writer, st *style, reports []detect.Report) {
	st.section(w, "Probe trace")
	for _, r := range reports {
		for _, t := range r.Tools {
			fmt.Fprintf(w, "%s  %s\n",
				st.render(st.bold, fmt.Sprintf("%s/%s", r.LanguageID, t.Name)),
				st.render(st.dim, fmt.Sprintf("(%s)", t.Class)))
			if len(t.ProbeChain) == 0 {
				fmt.Fprintf(w, "  %s\n", st.render(st.dim, "(no probe steps recorded)"))
				continue
			}
			for _, step := range t.ProbeChain {
				glyph := st.render(st.miss, "✗")
				if step.Found {
					glyph = st.render(st.ok, "✓")
				}
				line := step.Location
				if step.Reason != "" {
					line = fmt.Sprintf("%s  %s", step.Location, st.render(st.dim, "— "+step.Reason))
				}
				fmt.Fprintf(w, "  %s  %s\n", glyph, line)
			}
		}
	}
	fmt.Fprintln(w)
}

// ---------- Styling ----------

// style holds the lipgloss styles for the doctor renderer. When the
// output is not a TTY, render() short-circuits to plain text so pipes
// and redirects stay ANSI-free.
type style struct {
	enabled bool
	ok      lipgloss.Style
	miss    lipgloss.Style
	warn    lipgloss.Style
	dim     lipgloss.Style
	bold    lipgloss.Style
	head    lipgloss.Style
}

// newStyle returns a style configured for the writer. We only enable
// ANSI when w is *os.File AND a TTY — avoids leaking color into pipes,
// captured stdout in tests, and the e2e runner's buffered output.
func newStyle(w io.Writer) *style {
	st := &style{
		ok:   lipgloss.NewStyle().Foreground(lipgloss.Color("#1f883d")),
		miss: lipgloss.NewStyle().Foreground(lipgloss.Color("#cf222e")).Bold(true),
		warn: lipgloss.NewStyle().Foreground(lipgloss.Color("#9a6700")),
		dim:  lipgloss.NewStyle().Foreground(lipgloss.Color("#7d8590")),
		bold: lipgloss.NewStyle().Bold(true),
		head: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#5fafff")),
	}
	if f, ok := w.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		st.enabled = true
	}
	return st
}

func (s *style) render(sty lipgloss.Style, txt string) string {
	if !s.enabled {
		return txt
	}
	return sty.Render(txt)
}

// section emits a heading followed by a same-width underline. In
// styled mode the heading is bold-blue and the underline is dim; in
// plain mode both are plain text so the structure is still visible.
func (s *style) section(w io.Writer, title string) {
	fmt.Fprintln(w, s.render(s.head, title))
	fmt.Fprintln(w, s.render(s.dim, strings.Repeat("─", visibleWidth(title))))
}

// visibleWidth is the number of runes in s — used so the underline of
// a section heading matches the heading's printed width regardless of
// multi-byte characters.
func visibleWidth(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
