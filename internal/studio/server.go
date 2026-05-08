package studio

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
// All graph operations are forwarded to the daemon via the in-process
// jsonrpc.Service handle; Studio never touches the SQLite stores directly.
type Server struct {
	svc   *jsonrpc.Service
	token string
	mux   *http.ServeMux
}

// NewServer wires the routes and generates a one-shot token.
func NewServer(svc *jsonrpc.Service) (*Server, error) {
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
