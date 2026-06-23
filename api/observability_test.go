package api

import (
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollector(t *testing.T) {
	c := NewCollector()
	c.Record(QueryEvent{Tenant: "a", Outcome: outcomeOK, EstimatedCost: 5, Tokens: 1})
	c.Record(QueryEvent{Tenant: "a", Outcome: outcomeThrottled, EstimatedCost: 60, Tokens: 6})
	c.Record(QueryEvent{Tenant: "b", Outcome: outcomeOK, EstimatedCost: 200, Tokens: 20})

	snap := c.Snapshot()
	assert.EqualValues(t, 3, snap.TotalQueries)
	assert.EqualValues(t, 1, snap.TotalThrottled)

	byTenant := map[string]TenantUsage{}
	for _, tu := range snap.Tenants {
		byTenant[tu.Tenant] = tu
	}
	assert.EqualValues(t, 2, byTenant["a"].Queries)
	assert.EqualValues(t, 1, byTenant["a"].Throttled)
	assert.EqualValues(t, 7, byTenant["a"].TokensConsumed)
	assert.EqualValues(t, 20, byTenant["b"].TokensConsumed)

	// Cost buckets: 5 -> "0–10", 60 -> "50–100", 200 -> "100–500".
	counts := map[string]int64{}
	for _, b := range snap.CostBuckets {
		counts[b.Label] = b.Count
	}
	assert.EqualValues(t, 1, counts["0–10"])
	assert.EqualValues(t, 1, counts["50–100"])
	assert.EqualValues(t, 1, counts["100–500"])

	// Recent is newest-first.
	require.Len(t, snap.Recent, 3)
	assert.Equal(t, "b", snap.Recent[0].Tenant)
}

func TestCollector_RingBufferCap(t *testing.T) {
	c := NewCollector()
	for i := 0; i < maxRecentQueries+50; i++ {
		c.Record(QueryEvent{Tenant: "a", Outcome: outcomeOK, SQL: "q" + strconv.Itoa(i)})
	}
	snap := c.Snapshot()
	assert.Len(t, snap.Recent, maxRecentQueries, "feed is bounded")
	assert.EqualValues(t, maxRecentQueries+50, snap.TotalQueries, "totals are not bounded")
}

func TestStatsEndpoint(t *testing.T) {
	ts := newTestServer(t)
	_, _ = do(t, ts, http.MethodPost, "/query", "dev-key", `{"sql":"SELECT id FROM orders"}`)

	status, body := do(t, ts, http.MethodGet, "/stats", "dev-key", "")
	require.Equal(t, http.StatusOK, status)
	assert.EqualValues(t, 1, body["total_queries"])
	assert.NotEmpty(t, body["recent"])
}

func TestPlanEndpoint(t *testing.T) {
	ts := newTestServer(t)
	status, body := do(t, ts, http.MethodPost, "/plan", "dev-key",
		`{"sql":"SELECT name FROM orders WHERE amount > 100"}`)
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, body["label"], "Project")
	assert.NotNil(t, body["children"])
}

func TestMetricsEndpoint_PublicAndRecords(t *testing.T) {
	ts := newTestServer(t)
	_, _ = do(t, ts, http.MethodPost, "/query", "dev-key", `{"sql":"SELECT id FROM orders"}`)

	// /metrics needs no API key.
	resp, err := ts.Client().Get(ts.URL + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(raw), "sluice_queries_total")
}

func TestRequestIDHeader(t *testing.T) {
	ts := newTestServer(t)
	resp, err := ts.Client().Get(ts.URL + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEmpty(t, resp.Header.Get("X-Request-ID"))
}
