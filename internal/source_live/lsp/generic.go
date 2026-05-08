package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// genericDriver is the shared subprocess + JSON-RPC plumbing each
// concrete driver embeds. Language-specific behaviour (the executable
// name, argv, init options, qualified-name reconstruction, signature
// extraction) lives on the concrete type via the conf field.
type genericDriver struct {
	conf driverConf

	mu     sync.Mutex
	cmd    *exec.Cmd
	conn   *jsonrpcConn
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
	cancel context.CancelFunc
	root   string
	ready  bool
}

// driverConf carries the language-specific knobs.
type driverConf struct {
	languageID string
	executable string
	args       []string
	producedBy string
	initOpts   any
	// qualify reconstructs a qualified name from the parent path and
	// the symbol's own name. The default is dotted concatenation;
	// languages with different conventions (Go's pkg.Recv.Method,
	// Python's a.b.c) may override.
	qualify func(parents []string, name string) string
	// kindMap translates LSP SymbolKind ints into source_live kinds.
	// Drivers may override to encode language idioms (e.g.
	// Python "method" comes back as Function; tsserver uses Variable
	// for arrow-function fields).
	kindMap func(int) source_live.SymbolKind
	// postProcessSymbol is the per-driver normalization hook that
	// runs after the generic flattener has populated a Symbol but
	// before it is appended to the result. Drivers use it to align
	// shape with the tree-sitter / SCIP parsers — gopls in particular
	// emits Method symbols as flat top-level entries with names like
	// `(*Type).Method`, which the hook splits into Receiver + Name +
	// pkg-prefixed QualifiedName so the §6.12 canonical key matches
	// the tree-sitter side. fileBody carries the file's raw bytes so
	// drivers can derive package / module prefixes locally.
	postProcessSymbol func(sym *source_live.Symbol, fileBody []byte)
}

// newGenericDriver returns a driver wired for conf. It does not spawn
// the subprocess — that happens lazily on Initialize.
func newGenericDriver(conf driverConf) *genericDriver {
	if conf.qualify == nil {
		conf.qualify = defaultQualify
	}
	if conf.kindMap == nil {
		conf.kindMap = defaultKindMap
	}
	return &genericDriver{conf: conf}
}

// Language returns the canonical language id served by this driver.
func (d *genericDriver) Language() string { return d.conf.languageID }

// Initialize spawns the subprocess (if not already running) and sends
// the LSP initialize / initialized handshake. Idempotent on a Driver
// that has already transitioned to Ready.
func (d *genericDriver) Initialize(ctx context.Context, root string) error {
	d.mu.Lock()
	if d.ready {
		d.mu.Unlock()
		return nil
	}
	d.mu.Unlock()

	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}
	if d.conf.executable == "" {
		return fmt.Errorf("lsp: %s driver has no executable configured", d.conf.languageID)
	}
	if _, err := exec.LookPath(d.conf.executable); err != nil {
		return fmt.Errorf("lsp: %s executable %q not on $PATH: %w", d.conf.languageID, d.conf.executable, err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(runCtx, d.conf.executable, d.conf.args...) //nolint:gosec
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start %s: %w", d.conf.executable, err)
	}

	conn := newJSONRPC(stdin, stdout, nil)
	go conn.serve(runCtx)
	go drainStderr(stderr)

	d.mu.Lock()
	d.cmd = cmd
	d.conn = conn
	d.stdin = stdin
	d.stdout = stdout
	d.stderr = stderr
	d.cancel = cancel
	d.root = abs
	d.mu.Unlock()

	params := initializeParams{
		ProcessID:    -1,
		RootURI:      pathToURI(abs),
		Capabilities: map[string]any{},
	}
	if d.conf.initOpts != nil {
		// Some servers (pyright especially) read initializationOptions
		// from initialize. Carry them through verbatim.
		raw, err := json.Marshal(struct {
			initializeParams
			InitializationOptions any `json:"initializationOptions"`
		}{params, d.conf.initOpts})
		if err == nil {
			var combined map[string]any
			if err := json.Unmarshal(raw, &combined); err == nil {
				if _, callErr := conn.Call(ctx, "initialize", combined); callErr != nil {
					_ = d.terminate()
					return fmt.Errorf("initialize %s: %w", d.conf.languageID, callErr)
				}
				if err := conn.Notify("initialized", map[string]any{}); err != nil {
					_ = d.terminate()
					return fmt.Errorf("initialized notify: %w", err)
				}
				d.mu.Lock()
				d.ready = true
				d.mu.Unlock()
				return nil
			}
		}
	}

	if _, err := conn.Call(ctx, "initialize", params); err != nil {
		_ = d.terminate()
		return fmt.Errorf("initialize %s: %w", d.conf.languageID, err)
	}
	if err := conn.Notify("initialized", map[string]any{}); err != nil {
		_ = d.terminate()
		return fmt.Errorf("initialized notify: %w", err)
	}
	d.mu.Lock()
	d.ready = true
	d.mu.Unlock()
	return nil
}

// DocumentSymbol returns the symbol facts for path.
func (d *genericDriver) DocumentSymbol(ctx context.Context, path string) ([]source_live.Symbol, error) {
	if err := d.requireReady(); err != nil {
		return nil, err
	}
	abs := d.absPath(path)
	if err := d.didOpen(ctx, abs); err != nil {
		return nil, err
	}
	// Read file body once so per-driver postProcessSymbol hooks can
	// derive language-specific prefixes (Go package name, Python
	// module path) without an extra disk read per symbol.
	body, _ := os.ReadFile(abs) //nolint:gosec // path comes from caller-controlled root
	res, err := d.conn.Call(ctx, "textDocument/documentSymbol", documentSymbolParams{
		TextDocument: textDocumentIdentifier{URI: pathToURI(abs)},
	})
	if err != nil {
		return nil, fmt.Errorf("documentSymbol: %w", err)
	}
	if len(res) == 0 || string(res) == "null" {
		return nil, nil
	}
	// Some servers return SymbolInformation[] instead of DocumentSymbol[].
	// Try the rich shape first, then fall back to flat.
	var rich []documentSymbol
	if err := json.Unmarshal(res, &rich); err == nil && len(rich) > 0 && rich[0].SelectionRange != (rangeT{}) {
		return d.flattenDocumentSymbols(rich, path, nil, body), nil
	}
	var flat []symbolInformation
	if err := json.Unmarshal(res, &flat); err != nil {
		return nil, fmt.Errorf("documentSymbol: unmarshal: %w", err)
	}
	return d.flattenSymbolInformation(flat, path, body), nil
}

// symbolInformation is the legacy flat result shape some servers still
// return (or fall back to when the client doesn't advertise the
// documentSymbol capability).
type symbolInformation struct {
	Name          string `json:"name"`
	Kind          int    `json:"kind"`
	Location      location
	ContainerName string `json:"containerName,omitempty"`
}

func (d *genericDriver) flattenDocumentSymbols(syms []documentSymbol, path string, parents []string, fileBody []byte) []source_live.Symbol {
	out := make([]source_live.Symbol, 0, len(syms))
	for _, s := range syms {
		kind := d.conf.kindMap(s.Kind)
		qn := d.conf.qualify(parents, s.Name)
		sym := source_live.Symbol{
			Name:          s.Name,
			QualifiedName: qn,
			Kind:          kind,
			Range:         lspRange(s.Range),
			Signature:     strings.TrimSpace(s.Detail),
			LanguageID:    d.conf.languageID,
			Path:          path,
			SourceClass:   source_live.SourceClassLSP,
			ProducedBy:    d.conf.producedBy,
			Confidence:    0.9,
		}
		if kind == source_live.SymbolKindMethod && len(parents) > 0 {
			sym.Receiver = parents[len(parents)-1]
		}
		if d.conf.postProcessSymbol != nil {
			d.conf.postProcessSymbol(&sym, fileBody)
		}
		out = append(out, sym)
		if len(s.Children) > 0 {
			out = append(out, d.flattenDocumentSymbols(s.Children, path, append(parents, s.Name), fileBody)...)
		}
	}
	return out
}

func (d *genericDriver) flattenSymbolInformation(syms []symbolInformation, path string, fileBody []byte) []source_live.Symbol {
	out := make([]source_live.Symbol, 0, len(syms))
	for _, s := range syms {
		kind := d.conf.kindMap(s.Kind)
		var parents []string
		if s.ContainerName != "" {
			parents = strings.Split(s.ContainerName, ".")
		}
		sym := source_live.Symbol{
			Name:          s.Name,
			QualifiedName: d.conf.qualify(parents, s.Name),
			Kind:          kind,
			Range:         lspRange(s.Location.Range),
			LanguageID:    d.conf.languageID,
			Path:          path,
			SourceClass:   source_live.SourceClassLSP,
			ProducedBy:    d.conf.producedBy,
			Confidence:    0.85,
		}
		if kind == source_live.SymbolKindMethod && s.ContainerName != "" {
			sym.Receiver = s.ContainerName
		}
		if d.conf.postProcessSymbol != nil {
			d.conf.postProcessSymbol(&sym, fileBody)
		}
		out = append(out, sym)
	}
	return out
}

// Definition implements LSP textDocument/definition.
func (d *genericDriver) Definition(ctx context.Context, path string, line, character uint32) ([]Location, error) {
	if err := d.requireReady(); err != nil {
		return nil, err
	}
	abs := d.absPath(path)
	if err := d.didOpen(ctx, abs); err != nil {
		return nil, err
	}
	res, err := d.conn.Call(ctx, "textDocument/definition", textDocumentPositionParams{
		TextDocument: textDocumentIdentifier{URI: pathToURI(abs)},
		Position:     position{Line: line, Character: character},
	})
	if err != nil {
		return nil, fmt.Errorf("definition: %w", err)
	}
	return d.unmarshalLocations(res), nil
}

// References implements LSP textDocument/references.
func (d *genericDriver) References(ctx context.Context, path string, line, character uint32, includeDeclaration bool) ([]Location, error) {
	if err := d.requireReady(); err != nil {
		return nil, err
	}
	abs := d.absPath(path)
	if err := d.didOpen(ctx, abs); err != nil {
		return nil, err
	}
	res, err := d.conn.Call(ctx, "textDocument/references", referencesParams{
		TextDocument: textDocumentIdentifier{URI: pathToURI(abs)},
		Position:     position{Line: line, Character: character},
		Context:      referenceContext{IncludeDeclaration: includeDeclaration},
	})
	if err != nil {
		return nil, fmt.Errorf("references: %w", err)
	}
	return d.unmarshalLocations(res), nil
}

func (d *genericDriver) unmarshalLocations(res json.RawMessage) []Location {
	if len(res) == 0 || string(res) == "null" {
		return nil
	}
	var locs []location
	if err := json.Unmarshal(res, &locs); err != nil {
		return nil
	}
	out := make([]Location, 0, len(locs))
	for _, l := range locs {
		out = append(out, Location{
			Path:  uriToRelPath(l.URI, d.root),
			Range: lspRange(l.Range),
		})
	}
	return out
}

// Health returns nil if the subprocess is alive and responding to a
// dummy textDocument/documentSymbol request on a non-existent file.
// We use a short-deadline ctx to bound the check.
func (d *genericDriver) Health(ctx context.Context) error {
	d.mu.Lock()
	cmd, conn, ready := d.cmd, d.conn, d.ready
	d.mu.Unlock()
	if !ready || cmd == nil || conn == nil {
		return errors.New("driver not initialized")
	}
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		return fmt.Errorf("subprocess exited: %s", cmd.ProcessState.String())
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Most servers respond to documentSymbol on a non-existent URI with
	// either an empty result or a server-error; both confirm the
	// transport is alive.
	_, err := conn.Call(pingCtx, "textDocument/documentSymbol", documentSymbolParams{
		TextDocument: textDocumentIdentifier{URI: "file:///__healthcheck__"},
	})
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("health timeout: %w", err)
	}
	return nil
}

// Shutdown sends shutdown + exit, then reaps the subprocess. The exit
// notification per LSP spec is a fire-and-forget.
func (d *genericDriver) Shutdown(ctx context.Context) error {
	d.mu.Lock()
	conn := d.conn
	ready := d.ready
	d.mu.Unlock()
	if !ready || conn == nil {
		return d.terminate()
	}
	shutCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, _ = conn.Call(shutCtx, "shutdown", nil)
	_ = conn.Notify("exit", nil)
	return d.terminate()
}

func (d *genericDriver) terminate() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	if d.stdin != nil {
		_ = d.stdin.Close()
	}
	cmd := d.cmd
	d.cmd = nil
	d.conn = nil
	d.ready = false
	if cmd != nil && cmd.Process != nil {
		// Wait briefly so we don't leak zombies; a kill follows if the
		// server didn't honor `exit`.
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	return nil
}

func (d *genericDriver) requireReady() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.ready {
		return errors.New("lsp: driver not initialized; call Initialize first")
	}
	return nil
}

// didOpen sends a textDocument/didOpen for path so the server has the
// file's contents in scope. Idempotent at the protocol level — most
// servers accept repeated didOpen as a refresh.
func (d *genericDriver) didOpen(ctx context.Context, abs string) error {
	_ = ctx
	body, err := os.ReadFile(abs) //nolint:gosec // workspace path comes from caller-provided root
	if err != nil {
		return nil //nolint:nilerr
	}
	return d.conn.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        pathToURI(abs),
			"languageId": d.conf.languageID,
			"version":    1,
			"text":       string(body),
		},
	})
}

func (d *genericDriver) absPath(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(d.root, rel)
}

// drainStderr keeps the subprocess from blocking on a full stderr
// buffer. We don't surface server-side log lines on the events bus
// (yet); that's a P2 enhancement once we have a structured log sink.
func drainStderr(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n == 0 || err != nil {
			return
		}
	}
}

func pathToURI(p string) string {
	abs, _ := filepath.Abs(p)
	abs = filepath.ToSlash(abs)
	if !strings.HasPrefix(abs, "/") {
		abs = "/" + abs
	}
	u := &url.URL{Scheme: "file", Path: abs}
	return u.String()
}

func uriToRelPath(uri, root string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	if u.Scheme != "file" {
		return uri
	}
	abs := u.Path
	if root == "" {
		return abs
	}
	rel, err := filepath.Rel(root, filepath.FromSlash(abs))
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

func lspRange(r rangeT) source_live.Range {
	return source_live.Range{
		StartByte: 0,
		EndByte:   0,
		StartLine: r.Start.Line + 1,
		EndLine:   r.End.Line + 1,
	}
}

func defaultQualify(parents []string, name string) string {
	if len(parents) == 0 {
		return name
	}
	return strings.Join(append(parents, name), ".")
}

func defaultKindMap(k int) source_live.SymbolKind {
	switch k {
	case lspKindFile:
		return source_live.SymbolKindFile
	case lspKindModule:
		return source_live.SymbolKindModule
	case lspKindClass, lspKindStruct:
		return source_live.SymbolKindClass
	case lspKindInterface:
		return source_live.SymbolKindInterface
	case lspKindMethod, lspKindProperty:
		return source_live.SymbolKindMethod
	case lspKindFunction:
		return source_live.SymbolKindFunction
	case lspKindEnum, lspKindField, lspKindVariable, lspKindConstant:
		return source_live.SymbolKindSymbol
	default:
		return source_live.SymbolKindSymbol
	}
}
