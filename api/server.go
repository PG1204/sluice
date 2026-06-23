package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/PG1204/sluice/engine"
	"github.com/PG1204/sluice/limiter"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server is the HTTP service. It owns the engine, the rate limiter, and the
// auth/quota configuration, and exposes an http.Handler with all routes.
type Server struct {
	engine       *engine.Engine
	limiter      limiter.Limiter
	limiterCfg   limiter.Config
	keyToTenant  map[string]string
	costPerToken float64
	metrics      *metrics
	collector    *Collector
	log          *slog.Logger
}

// NewServer builds a Server from config. It wires an in-memory token-bucket
// limiter (the Redis-backed limiter can be swapped in later); the engine reads
// tables from the configured data directory.
func NewServer(cfg Config, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	lc := cfg.limiterConfig()
	costPerToken := cfg.CostPerToken
	if costPerToken <= 0 {
		costPerToken = DefaultCostPerToken
	}
	return &Server{
		engine:       engine.New(cfg.DataDir),
		limiter:      limiter.NewTokenBucket(lc),
		limiterCfg:   lc,
		keyToTenant:  cfg.keyToTenant(),
		costPerToken: costPerToken,
		metrics:      newMetrics(),
		collector:    NewCollector(),
		log:          log,
	}
}

// Handler returns the routed http.Handler. /health and /metrics are public;
// every other endpoint requires a valid API key. The whole mux is wrapped in
// request-ID middleware so every request (and its logs) is traceable.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.metrics.registry, promhttp.HandlerOpts{}))
	mux.Handle("POST /query", s.authed(s.handleQuery))
	mux.Handle("POST /explain", s.authed(s.handleExplain))
	mux.Handle("POST /plan", s.authed(s.handlePlan))
	mux.Handle("GET /tables", s.authed(s.handleTables))
	mux.Handle("GET /quota", s.authed(s.handleQuota))
	mux.Handle("GET /stats", s.authed(s.handleStats))
	return s.withCORS(s.withRequestID(mux))
}

// withCORS allows the browser dashboard (served from a different origin in dev)
// to call the API. It is permissive by design for a local/demo service;
// production would restrict the allowed origin.
func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requestIDContextKey carries the per-request trace ID.
type requestIDContextKey struct{}

// withRequestID assigns each request a trace ID, echoes it in the X-Request-ID
// response header, and stores it in the context for handlers and logs.
func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}

// newRequestID returns a random 16-hex-char trace ID (no external uuid dep).
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
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
