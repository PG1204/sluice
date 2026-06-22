package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/PG1204/sluice/engine"
	"github.com/PG1204/sluice/limiter"
)

// Server is the HTTP service. It owns the engine, the rate limiter, and the
// auth/quota configuration, and exposes an http.Handler with all routes.
type Server struct {
	engine      *engine.Engine
	limiter     limiter.Limiter
	limiterCfg  limiter.Config
	keyToTenant map[string]string
	log         *slog.Logger
}

// NewServer builds a Server from config. It wires an in-memory token-bucket
// limiter (the Redis-backed limiter can be swapped in later); the engine reads
// tables from the configured data directory.
func NewServer(cfg Config, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	lc := cfg.limiterConfig()
	return &Server{
		engine:      engine.New(cfg.DataDir),
		limiter:     limiter.NewTokenBucket(lc),
		limiterCfg:  lc,
		keyToTenant: cfg.keyToTenant(),
		log:         log,
	}
}

// Handler returns the routed http.Handler. /health is public; every other
// endpoint requires a valid API key.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.Handle("POST /query", s.authed(s.handleQuery))
	mux.Handle("POST /explain", s.authed(s.handleExplain))
	mux.Handle("GET /tables", s.authed(s.handleTables))
	mux.Handle("GET /quota", s.authed(s.handleQuota))
	return mux
}

// tenantContextKey carries the authenticated tenant through the request.
type tenantContextKey struct{}

// authed wraps a handler with API-key authentication, attaching the resolved
// tenant to the request context.
func (s *Server) authed(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		tenant, ok := s.keyToTenant[key]
		if key == "" || !ok {
			writeError(w, http.StatusUnauthorized, "missing or invalid API key")
			return
		}
		ctx := contextWithTenant(r.Context(), tenant)
		next(w, r.WithContext(ctx))
	})
}

// --- JSON helpers ---

// writeJSON encodes v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// errorResponse is the body of every error reply.
type errorResponse struct {
	Error string `json:"error"`
}

// writeError sends a JSON error with the given status code.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

// decodeJSON reads a JSON request body into v, rejecting unknown fields.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
