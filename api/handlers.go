package api

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"time"
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

// handleQuery is the cost-based throttling path — the heart of Sluice. It
// prepares the query (which estimates its cost), charges that cost against the
// tenant's quota, and only executes if the tenant can afford it. An expensive
// query therefore draws down quota faster than a cheap one, so it gets
// throttled sooner at the same request rate.
//
// Ordering matters: all input errors surface in Prepare (a 400) *before* any
// tokens are charged; only a tenant that's out of quota gets a 429.
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	var req sqlRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.SQL == "" {
		writeError(w, http.StatusBadRequest, "sql is required")
		return
	}
	tenant := tenantFromContext(r.Context())
	reqID := requestIDFromContext(r.Context())

	// 1. Prepare: parse, plan, optimize, estimate cost (cheap, no execution).
	prepared, err := s.engine.Prepare(r.Context(), req.SQL)
	if err != nil {
		s.record(reqID, tenant, req.SQL, outcomeError, 0, 0, 0, time.Since(start))
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 2. Map the estimated cost to tokens and try to consume them.
	cost := prepared.Cost()
	tokens := tokensForCost(cost, s.costPerToken)
	res, err := s.limiter.Allow(r.Context(), tenant, tokens)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 3. Out of quota -> 429, with retry/cost info so the caller understands.
	if !res.Allowed {
		retrySec := int(math.Ceil(res.RetryAfter.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(retrySec))
		s.record(reqID, tenant, req.SQL, outcomeThrottled, cost, tokens, 0, time.Since(start))
		s.log.Info("query throttled",
			"request_id", reqID, "tenant", tenant, "estimated_cost", cost,
			"tokens", tokens, "remaining", res.Remaining, "retry_after_s", retrySec)
		writeJSON(w, http.StatusTooManyRequests, throttleResponse{
			Error:             "rate limit exceeded",
			EstimatedCost:     cost,
			TokensRequired:    tokens,
			Remaining:         res.Remaining,
			RetryAfterSeconds: retrySec,
		})
		return
	}

	// 4. Allowed: execute and return results.
	result, err := s.engine.Execute(r.Context(), prepared)
	if err != nil {
		s.record(reqID, tenant, req.SQL, outcomeError, cost, tokens, 0, time.Since(start))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	latency := time.Since(start)
	s.record(reqID, tenant, req.SQL, outcomeOK, cost, tokens, int64(result.RowCount()), latency)
	s.log.Info("query ok",
		"request_id", reqID, "tenant", tenant, "estimated_cost", cost,
		"tokens", tokens, "remaining", res.Remaining, "rows", result.RowCount(),
		"latency_ms", latency.Milliseconds())
	writeJSON(w, http.StatusOK, newQueryResponse(result))
}

// record observes one query in both Prometheus and the dashboard collector.
func (s *Server) record(reqID, tenant, sql, outcome string, cost float64, tokens, rows int64, latency time.Duration) {
	s.metrics.observe(tenant, outcome, tokens, latency.Seconds())
	s.collector.Record(QueryEvent{
		RequestID:     reqID,
		Time:          nowRFC3339(),
		Tenant:        tenant,
		SQL:           sql,
		Outcome:       outcome,
		EstimatedCost: cost,
		Tokens:        tokens,
		Rows:          rows,
		LatencyMs:     latency.Milliseconds(),
	})
}

// nowRFC3339 is the current UTC time as an RFC3339 string for event timestamps.
func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

// tokensForCost maps an estimated query cost to the number of tokens to charge,
// rounding up and charging at least one token per query.
func tokensForCost(cost, costPerToken float64) int64 {
	if costPerToken <= 0 {
		costPerToken = DefaultCostPerToken
	}
	tokens := int64(math.Ceil(cost / costPerToken))
	if tokens < 1 {
		return 1
	}
	return tokens
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

// handlePlan returns the optimized plan as a tree (label + estimated rows/cost
// per node), for the dashboard's plan visualizer.
func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	var req sqlRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.SQL == "" {
		writeError(w, http.StatusBadRequest, "sql is required")
		return
	}
	tree, err := s.engine.PlanTree(r.Context(), req.SQL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tree)
}

// handleStats returns the dashboard snapshot: totals, per-tenant usage, the
// cost histogram, and the recent-query feed.
func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.collector.Snapshot())
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
