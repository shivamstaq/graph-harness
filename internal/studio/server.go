package studio

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/shivamstaq/graph-harness/internal/jsonrpc"
)

// Embedded SPA. The build pipeline keeps this directory simple HTML/CSS/JS
// today; SolidJS / React migration is deferred.
//
//go:embed spa
var spaFS embed.FS

// Server is the Studio HTTP server. One per `graph-harness studio` invocation.
//
// Security:
//   - Listener binds 127.0.0.1 only (Listen never resolves 0.0.0.0).
//   - Every request must carry the per-session token in either the
//     X-Studio-Token header or `?token=` query string.
//   - Origin (when present) must be loopback (http://127.0.0.1:* or
//     http://localhost:*).
//
// All graph operations are forwarded to the daemon via the
// jsonrpc.Consumer interface. Concrete shape may be either the
// in-process *jsonrpc.Service (no daemon running) or a
// *jsonrpc.ClientService (daemon running and Studio dials it
// over JSON-RPC). Studio never touches the SQLite stores directly.
type Server struct {
	svc   jsonrpc.Consumer
	token string
	mux   *http.ServeMux
}

// NewServer wires the routes and generates a one-shot token.
func NewServer(svc jsonrpc.Consumer) (*Server, error) {
	if svc == nil {
		return nil, errors.New("nil service")
	}
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	s := &Server{svc: svc, token: hex.EncodeToString(tok), mux: http.NewServeMux()}
	s.routes()
	return s, nil
}

// Token returns the per-session token. Print this in the URL banner.
func (s *Server) Token() string { return s.token }

// Listen binds 127.0.0.1:<port>. port=0 = ephemeral.
func (s *Server) Listen(port int) (net.Listener, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	return net.Listen("tcp", addr)
}

// Serve runs the HTTP server until ln closes or ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// URL is the canonical browser link with token attached.
func (s *Server) URL(addr string) string {
	return fmt.Sprintf("http://%s/?token=%s", addr, s.token)
}

func (s *Server) routes() {
	spa, err := fs.Sub(spaFS, "spa")
	if err != nil {
		panic(err) // unreachable: //go:embed root
	}
	s.mux.HandleFunc("/healthz", s.handle(s.healthz))
	s.mux.HandleFunc("/api/status", s.handle(s.apiStatus))
	s.mux.HandleFunc("/api/selectors/preview", s.handle(s.apiSelectorPreview))
	s.mux.HandleFunc("/api/selectors/test", s.handle(s.apiSelectorTest))
	s.mux.HandleFunc("/api/overlay/save", s.handle(s.apiOverlaySave))
	s.mux.HandleFunc("/api/conflicts", s.handle(s.apiConflicts))
	s.mux.HandleFunc("/api/entity/provenance", s.handle(s.apiEntityProvenance))
	// P2.T38 framework-entity browser endpoints. One thin shim per
	// framework.* JSON-RPC method so the SPA can use plain fetch();
	// each forwards the same FrameworkListParams envelope through
	// the Consumer adapter so in-process *Service and remote
	// *ClientService both work without a code split.
	s.mux.HandleFunc("/api/framework/routes", s.handle(s.apiFrameworkRoutes))
	s.mux.HandleFunc("/api/framework/events", s.handle(s.apiFrameworkEvents))
	s.mux.HandleFunc("/api/framework/event_publishers", s.handle(s.apiFrameworkEventPublishers))
	s.mux.HandleFunc("/api/framework/event_subscribers", s.handle(s.apiFrameworkEventSubscribers))
	s.mux.HandleFunc("/api/framework/schemas", s.handle(s.apiFrameworkSchemas))
	s.mux.HandleFunc("/api/framework/schema_fields", s.handle(s.apiFrameworkSchemaFields))
	s.mux.HandleFunc("/api/framework/tests", s.handle(s.apiFrameworkTests))
	s.mux.HandleFunc("/api/framework/contract_tests", s.handle(s.apiFrameworkContractTests))
	s.mux.HandleFunc("/api/framework/steps_touching", s.handle(s.apiFrameworkStepsTouching))
	s.mux.Handle("/", http.StripPrefix("/", s.guarded(http.FileServer(http.FS(spa)))))
}

// handle wraps an api handler with auth + origin check + JSON envelope.
func (s *Server) handle(fn func(w http.ResponseWriter, r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.checkAuth(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		out, err := fn(w, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if out != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		}
	}
}

// guarded is the SPA-asset wrapper: enforces token + origin even on static
// content so a stray fetch to /index.html from a non-loopback origin is
// rejected.
func (s *Server) guarded(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.checkAuth(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) checkAuth(r *http.Request) bool {
	q := r.URL.Query().Get("token")
	h := r.Header.Get("X-Studio-Token")
	if q != s.token && h != s.token {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if !strings.HasPrefix(origin, "http://127.0.0.1") && !strings.HasPrefix(origin, "http://localhost") {
		return false
	}
	return true
}

// --- handlers --------------------------------------------------------------

func (s *Server) healthz(_ http.ResponseWriter, _ *http.Request) (any, error) {
	return map[string]string{"status": "ok"}, nil
}

func (s *Server) apiStatus(_ http.ResponseWriter, r *http.Request) (any, error) {
	return s.svc.Status(r.Context())
}

func (s *Server) apiSelectorPreview(_ http.ResponseWriter, r *http.Request) (any, error) {
	var p jsonrpc.SelectorsPreviewParams
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return s.svc.SelectorsPreview(r.Context(), p)
}

func (s *Server) apiSelectorTest(_ http.ResponseWriter, r *http.Request) (any, error) {
	var p jsonrpc.SelectorsTestParams
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return s.svc.SelectorsTest(r.Context(), p)
}

func (s *Server) apiOverlaySave(_ http.ResponseWriter, r *http.Request) (any, error) {
	var p jsonrpc.OverlaySaveParams
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return s.svc.OverlaySave(r.Context(), p)
}

func (s *Server) apiConflicts(_ http.ResponseWriter, r *http.Request) (any, error) {
	return s.svc.ConflictsList(r.Context())
}

func (s *Server) apiEntityProvenance(_ http.ResponseWriter, r *http.Request) (any, error) {
	var p jsonrpc.EntityProvenanceParams
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return s.svc.EntityProvenance(r.Context(), p)
}

// --- P2.T38 framework-entity browsers --------------------------------------

// decodeFrameworkListParams decodes the shared list-params envelope.
// Tolerates an empty body (no params) by returning the zero value so
// the SPA can issue a GET-like POST with no JSON for the "list
// everything" page.
func decodeFrameworkListParams(r *http.Request) (jsonrpc.FrameworkListParams, error) {
	var p jsonrpc.FrameworkListParams
	if r.Body == nil {
		return p, nil
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		// Empty body is fine; everything else is a real error.
		if errors.Is(err, io.EOF) {
			return p, nil
		}
		return p, fmt.Errorf("decode: %w", err)
	}
	return p, nil
}

func (s *Server) apiFrameworkRoutes(_ http.ResponseWriter, r *http.Request) (any, error) {
	p, err := decodeFrameworkListParams(r)
	if err != nil {
		return nil, err
	}
	return s.svc.FrameworkRoutes(r.Context(), p)
}

func (s *Server) apiFrameworkEvents(_ http.ResponseWriter, r *http.Request) (any, error) {
	p, err := decodeFrameworkListParams(r)
	if err != nil {
		return nil, err
	}
	return s.svc.FrameworkEvents(r.Context(), p)
}

func (s *Server) apiFrameworkEventPublishers(_ http.ResponseWriter, r *http.Request) (any, error) {
	p, err := decodeFrameworkListParams(r)
	if err != nil {
		return nil, err
	}
	return s.svc.FrameworkEventPublishers(r.Context(), p)
}

func (s *Server) apiFrameworkEventSubscribers(_ http.ResponseWriter, r *http.Request) (any, error) {
	p, err := decodeFrameworkListParams(r)
	if err != nil {
		return nil, err
	}
	return s.svc.FrameworkEventSubscribers(r.Context(), p)
}

func (s *Server) apiFrameworkSchemas(_ http.ResponseWriter, r *http.Request) (any, error) {
	p, err := decodeFrameworkListParams(r)
	if err != nil {
		return nil, err
	}
	return s.svc.FrameworkSchemas(r.Context(), p)
}

func (s *Server) apiFrameworkSchemaFields(_ http.ResponseWriter, r *http.Request) (any, error) {
	p, err := decodeFrameworkListParams(r)
	if err != nil {
		return nil, err
	}
	return s.svc.FrameworkSchemaFields(r.Context(), p)
}

func (s *Server) apiFrameworkTests(_ http.ResponseWriter, r *http.Request) (any, error) {
	p, err := decodeFrameworkListParams(r)
	if err != nil {
		return nil, err
	}
	return s.svc.FrameworkTests(r.Context(), p)
}

func (s *Server) apiFrameworkContractTests(_ http.ResponseWriter, r *http.Request) (any, error) {
	p, err := decodeFrameworkListParams(r)
	if err != nil {
		return nil, err
	}
	return s.svc.FrameworkContractTests(r.Context(), p)
}

func (s *Server) apiFrameworkStepsTouching(_ http.ResponseWriter, r *http.Request) (any, error) {
	var p jsonrpc.FrameworkStepsTouchingParams
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("decode: %w", err)
		}
	}
	return s.svc.FrameworkStepsTouching(r.Context(), p)
}
