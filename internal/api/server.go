package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/fvsever/jiskta-ais/internal/auth"
	"github.com/fvsever/jiskta-ais/internal/store"
)

// Authenticator is the auth interface used by the HTTP handlers.
// *auth.SupabaseAuth satisfies this interface.
type Authenticator interface {
	ValidateKey(rawKey string) (*auth.KeyInfo, error)
	UpdateCachedBalance(rawKey string, cost int64)
	DeductCredits(apiKeyID string, cost int64, rawKey string)
}

// Server wires together the HTTP routes with auth and the core store.
type Server struct {
	mux    *http.ServeMux
	auth   Authenticator
	store  *store.CoreClient
}

// NewServer returns a configured Server using the concrete Supabase auth provider.
func NewServer(a *auth.SupabaseAuth, c *store.CoreClient) *Server {
	return NewServerWithAuth(a, c)
}

// NewServerWithAuth returns a configured Server accepting any Authenticator
// implementation.  This is the injection point used by tests (mock auth).
func NewServerWithAuth(a Authenticator, c *store.CoreClient) *Server {
	s := &Server{mux: http.NewServeMux(), auth: a, store: c}
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/api/v1/ais/query", s.authMiddleware(s.handleQuery))
	s.mux.HandleFunc("/api/v1/ais/vessel/", s.authMiddleware(s.handleVessel))
	s.mux.HandleFunc("/api/v1/ais/live", s.authMiddleware(s.handleLive))
	s.mux.HandleFunc("/api/v1/ais/coverage", s.handleCoverage)
	return s
}

// Handler returns the underlying http.Handler.
func (s *Server) Handler() http.Handler { return s.mux }

// Shutdown gracefully stops the server (currently a no-op placeholder).
func (s *Server) Shutdown(_ context.Context) error { return nil }

// authMiddleware validates X-API-Key and injects KeyInfo into the request context.
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		if key == "" {
			key = r.URL.Query().Get("api_key")
		}
		if key == "" {
			http.Error(w, `{"error":"missing API key"}`, http.StatusUnauthorized)
			return
		}
		ki, err := s.auth.ValidateKey(key)
		if err != nil {
			http.Error(w, `{"error":"invalid or inactive API key"}`, http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyInfo{}, ki)
		next(w, r.WithContext(ctx))
	}
}

type ctxKeyInfo struct{}

func keyInfoFromCtx(r *http.Request) *auth.KeyInfo {
	ki, _ := r.Context().Value(ctxKeyInfo{}).(*auth.KeyInfo)
	return ki
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"healthy","service":"jiskta-ais"}`))
}

// mmsiFromPath extracts the MMSI from a path like /api/v1/ais/vessel/123456789
func mmsiFromPath(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/ais/vessel/"), "/")
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}
