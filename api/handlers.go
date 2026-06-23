package api

import (
	"context"
	"net/http"
)

// contextWithTenant / tenantFromContext carry the authenticated tenant through
// a request after the auth middleware resolves it.
func contextWithTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, tenantContextKey{}, tenant)
}

func tenantFromContext(ctx context.Context) string {
	tenant, _ := ctx.Value(tenantContextKey{}).(string)
	return tenant
}

// handleHealth reports liveness and version. Public (no auth).
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthBody())
}

// handleQuery runs a SQL query and returns rows as JSON. A bad query (parse,
// validation, or unknown table) is a 400; the SQL is the client's input.
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req sqlRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.SQL == "" {
		writeError(w, http.StatusBadRequest, "sql is required")
		return
	}

	result, err := s.engine.Query(r.Context(), req.SQL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, newQueryResponse(result))
}

// handleExplain returns the optimized plan (with per-operator cost) and the
// total estimated cost.
func (s *Server) handleExplain(w http.ResponseWriter, r *http.Request) {
	var req sqlRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.SQL == "" {
		writeError(w, http.StatusBadRequest, "sql is required")
		return
	}

	plan, err := s.engine.ExplainCost(r.Context(), req.SQL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cost, err := s.engine.Cost(r.Context(), req.SQL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, explainResponse{Plan: plan, Cost: cost})
}

// handleTables lists available tables and their schemas.
func (s *Server) handleTables(w http.ResponseWriter, r *http.Request) {
	names, err := s.engine.Tables()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := tablesResponse{Tables: make([]tableInfo, 0, len(names))}
	for _, name := range names {
		schema, err := s.engine.TableSchema(r.Context(), name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp.Tables = append(resp.Tables, tableInfo{Name: name, Columns: schemaColumns(schema)})
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleQuota reports the caller's current quota. It peeks at the bucket with a
// zero-cost Allow, which never consumes tokens.
func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFromContext(r.Context())
	res, err := s.limiter.Allow(r.Context(), tenant, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	q := s.limiterCfg.QuotaFor(tenant)
	writeJSON(w, http.StatusOK, quotaResponse{
		Tenant:    tenant,
		Remaining: res.Remaining,
		Rate:      q.Rate,
		Burst:     q.Burst,
	})
}
