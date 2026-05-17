package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/sourcegraph/jsonrpc2"
)

// Server is the JSON-RPC 2.0 server hosted by the daemon. One Server per
// daemon process; many concurrent client connections are accepted on the
// underlying net.Listener.
//
// Method dispatch is capability-gated: a method is reachable only if its
// required capability tag appears in the registered capability set. Methods
// without a capability tag are always reachable (RPC-level housekeeping).
type Server struct {
	svc *Service

	mu       sync.Mutex
	methods  map[string]methodEntry
	caps     map[string]struct{} // capability set, union over installed layers
	listener net.Listener

	// activeConns tracks live jsonrpc2.Conn so Stop() can close them.
	connMu      sync.Mutex
	activeConns map[*jsonrpc2.Conn]struct{}
}

// methodEntry pairs a handler with its required capability (empty = always on).
type methodEntry struct {
	cap string
	// handler is the standard request-response shape used by every
	// method that does not need the underlying jsonrpc2.Conn.
	handler func(ctx context.Context, raw json.RawMessage) (any, error)
	// connHandler is the conn-aware variant used by the subscription
	// surface (kernel.subscribe / kernel.unsubscribe / kernel.identify
	// per SPEC §6.22). When non-nil it takes priority over handler.
	// The conn is needed so server-initiated notifications can be
	// pushed back to *this* client; capability gating still runs the
	// same way as for plain handlers.
	connHandler func(ctx context.Context, conn *jsonrpc2.Conn, raw json.RawMessage) (any, error)
}

// NewServer constructs a Server bound to the given Service. Default
// capabilities cover the P0 baseline + the P1 surface methods. Callers
// can extend with AddCapability before Serve().
func NewServer(svc *Service) *Server {
	s := &Server{
		svc:         svc,
		methods:     map[string]methodEntry{},
		caps:        map[string]struct{}{},
		activeConns: map[*jsonrpc2.Conn]struct{}{},
	}
	// P0 baseline capabilities — every workspace gets these regardless of
	// which optional layers are installed (kernel + code.core +
	// semantic.overlay + change.process + review.queue ship in P0).
	for _, c := range []string{
		"daemon", "status", "layers",
		"selectors", "flows", "query", "validate",
		"review", "overlay",
		// P1 additions
		"mcp", "conflicts",
		// P0.5 additions — subscription substrate (SPEC §6.22).
		"kernel",
	} {
		s.AddCapability(c)
	}
	s.registerBuiltins()
	return s
}

// AddCapability marks a capability tag as installed. Methods gated on the
// tag become reachable.
func (s *Server) AddCapability(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.caps[name] = struct{}{}
}

// HasCapability reports whether the given capability tag is installed.
func (s *Server) HasCapability(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.caps[name]
	return ok
}

// Methods returns the sorted list of method names currently reachable
// given the installed capability set. Used by rpc.discover.
func (s *Server) Methods() []MethodDescriptor {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MethodDescriptor, 0, len(s.methods))
	for name, m := range s.methods {
		if m.cap != "" {
			if _, ok := s.caps[m.cap]; !ok {
				continue
			}
		}
		out = append(out, MethodDescriptor{Name: name, Capability: m.cap})
	}
	sortMethods(out)
	return out
}

// MethodDescriptor describes one reachable method.
type MethodDescriptor struct {
	Name       string `json:"name"`
	Capability string `json:"capability,omitempty"`
}

// Register binds a typed handler to a method name. The handler is
// instantiated for each request; params are decoded from raw JSON into a
// freshly allocated P. P must be a struct or a value type.
func Register[P any, R any](s *Server, method, capName string, fn func(ctx context.Context, p P) (R, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.methods[method] = methodEntry{
		cap: capName,
		handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var p P
			if len(raw) > 0 && string(raw) != "null" {
				if err := json.Unmarshal(raw, &p); err != nil {
					return nil, &jsonrpc2.Error{Code: -32602, Message: fmt.Sprintf("invalid params: %v", err)}
				}
			}
			return fn(ctx, p)
		},
	}
}

// noParams is the param shape for methods that take no arguments.
type noParams struct{}

// RegisterVoid binds a handler with no params.
func RegisterVoid[R any](s *Server, method, capName string, fn func(ctx context.Context) (R, error)) {
	Register[noParams, R](s, method, capName, func(ctx context.Context, _ noParams) (R, error) {
		return fn(ctx)
	})
}

// registerBuiltins wires every method described in doc.go.
func (s *Server) registerBuiltins() {
	svc := s.svc

	// rpc.discover — always reachable, no capability tag.
	s.mu.Lock()
	s.methods["rpc.discover"] = methodEntry{
		handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return map[string]any{
				"methods":      s.Methods(),
				"capabilities": s.capList(),
			}, nil
		},
	}
	s.mu.Unlock()

	RegisterVoid(s, "daemon.ping", "daemon", svc.Ping)
	RegisterVoid(s, "daemon.shutdown", "daemon", svc.DaemonShutdown)

	RegisterVoid(s, "status", "status", svc.Status)
	RegisterVoid(s, "layers.list", "layers", svc.LayersList)

	Register(s, "selectors.test", "selectors", svc.SelectorsTest)
	Register(s, "selectors.preview", "selectors", svc.SelectorsPreview)

	RegisterVoid(s, "flows.list", "flows", svc.FlowsList)

	Register(s, "query.parse", "query", svc.QueryParse)
	Register(s, "validate.diff", "validate", svc.ValidateDiff)

	Register(s, "review.list", "review", svc.ReviewList)
	Register(s, "review.get", "review", svc.ReviewGet)
	Register(s, "review.accept", "review", svc.ReviewAccept)
	Register(s, "review.reject", "review", svc.ReviewReject)

	Register(s, "overlay.save", "overlay", svc.OverlaySave)

	Register(s, "entity.provenance", "selectors", svc.EntityProvenance)

	Register(s, "mcp.before_edit", "mcp", svc.MCPBeforeEdit)
	Register(s, "mcp.after_edit", "mcp", svc.MCPAfterEdit)

	RegisterVoid(s, "conflicts.list", "conflicts", svc.ConflictsList)
	RegisterVoid(s, "health.extractors", "doctor", svc.DoctorReport)

	// SPEC §6.22 long-lived subscriber contract: kernel.identify /
	// subscribe / ack / unsubscribe. The subscribe + identify
	// methods need the underlying jsonrpc2.Conn so server-initiated
	// notifications push back to *this* client — registered via
	// registerConn rather than plain Register.
	registerConn(s, "kernel.identify", "kernel", svc.Identify)
	registerConn(s, "kernel.subscribe", "kernel", svc.Subscribe)
	Register(s, "kernel.ack", "kernel", svc.Ack)
	Register(s, "kernel.unsubscribe", "kernel", svc.Unsubscribe)
}

func (s *Server) capList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.caps))
	for c := range s.caps {
		out = append(out, c)
	}
	sortStrings(out)
	return out
}

// Serve accepts connections on the given listener and dispatches JSON-RPC
// messages until ctx is cancelled or the listener closes.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.serveConn(ctx, conn)
	}
}

// serveConn wraps a net.Conn in a jsonrpc2.Conn and handles requests until
// the client disconnects.
func (s *Server) serveConn(ctx context.Context, raw net.Conn) {
	stream := jsonrpc2.NewBufferedStream(raw, jsonrpc2.VSCodeObjectCodec{})
	var conn *jsonrpc2.Conn
	handler := jsonrpc2.HandlerWithError(func(ctx context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
		return s.dispatchWithConn(ctx, conn, req)
	})
	conn = jsonrpc2.NewConn(ctx, stream, handler)

	s.connMu.Lock()
	s.activeConns[conn] = struct{}{}
	s.connMu.Unlock()

	<-conn.DisconnectNotify()

	// On disconnect, drop every subscription this connection owned so
	// the per-connection event-pump goroutines exit and their event-
	// stream cursors release (SPEC §6.22: subscription state may
	// persist beyond disconnect for reconnect, but the in-memory
	// fan-out tied to this concrete conn must terminate).
	if s.svc != nil {
		s.svc.subscriptions().DropConn(conn)
	}

	s.connMu.Lock()
	delete(s.activeConns, conn)
	s.connMu.Unlock()
}

// dispatch resolves the method, enforces capability gating, and invokes
// the standard request-response handler. Subscription methods need
// access to the underlying jsonrpc2.Conn so server-initiated
// notifications can be pushed back; for those, see dispatchWithConn.
func (s *Server) dispatch(ctx context.Context, req *jsonrpc2.Request) (any, error) {
	return s.dispatchWithConn(ctx, nil, req)
}

func (s *Server) dispatchWithConn(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
	s.svc.Touch()

	s.mu.Lock()
	m, ok := s.methods[req.Method]
	s.mu.Unlock()
	if !ok {
		return nil, &jsonrpc2.Error{Code: -32601, Message: fmt.Sprintf("method %q not found", req.Method)}
	}
	if m.cap != "" {
		if !s.HasCapability(m.cap) {
			return nil, &jsonrpc2.Error{
				Code:    -32601,
				Message: fmt.Sprintf("method %q gated on capability %q which is not installed", req.Method, m.cap),
			}
		}
	}
	var raw json.RawMessage
	if req.Params != nil {
		raw = *req.Params
	}
	if m.connHandler != nil {
		if conn == nil {
			return nil, &jsonrpc2.Error{
				Code:    -32603,
				Message: fmt.Sprintf("method %q requires a live JSON-RPC connection", req.Method),
			}
		}
		return m.connHandler(ctx, conn, raw)
	}
	return m.handler(ctx, raw)
}

// Stop closes the listener and all active connections.
func (s *Server) Stop() {
	s.mu.Lock()
	ln := s.listener
	s.listener = nil
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	s.connMu.Lock()
	for c := range s.activeConns {
		_ = c.Close()
	}
	s.connMu.Unlock()
}

// sortMethods sorts MethodDescriptor by Name. Local helper to avoid
// importing sort just for this (the file would otherwise have one import).
func sortMethods(s []MethodDescriptor) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1].Name > s[j].Name; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// sortStrings — same rationale.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
