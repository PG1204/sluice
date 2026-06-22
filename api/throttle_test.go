package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serverWith builds a test server from a custom config.
func serverWith(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(NewServer(cfg, nil).Handler())
	t.Cleanup(ts.Close)
	return ts
}

// countAllowed fires n /query requests and returns how many were admitted (200)
// vs throttled (429).
func countAllowed(t *testing.T, ts *httptest.Server, key, sql string, n int) (allowed, throttled int) {
	t.Helper()
	body := `{"sql":` + quote(sql) + `}`
	for i := 0; i < n; i++ {
		status, _ := do(t, ts, http.MethodPost, "/query", key, body)
		switch status {
		case http.StatusOK:
			allowed++
		case http.StatusTooManyRequests:
			throttled++
		default:
			t.Fatalf("unexpected status %d", status)
		}
	}
	return allowed, throttled
}

func quote(s string) string { return `"` + s + `"` }

// TestThrottle_ExpensiveThrottledFasterThanCheap is the headline behavior: at
// the same request count and quota, the tenant running expensive queries is
// admitted fewer times than the one running cheap queries, because each
// expensive query draws more tokens.
func TestThrottle_ExpensiveThrottledFasterThanCheap(t *testing.T) {
	cfg := Config{
		DataDir:      "../testdata",
		DefaultQuota: Quota{Rate: 0, Burst: 200}, // rate 0: no refill, so the test is deterministic
		CostPerToken: 1,
		APIKeys: map[string]KeyConfig{
			"cheap-key": {Tenant: "cheap"},
			"exp-key":   {Tenant: "exp"},
		},
	}
	ts := serverWith(t, cfg)

	const requests = 60
	cheapSQL := "SELECT id FROM orders LIMIT 1"
	expensiveSQL := "SELECT o.name, COUNT(*) FROM orders o JOIN customers c ON o.name = c.name GROUP BY o.name"

	cheapAllowed, cheapThrottled := countAllowed(t, ts, "cheap-key", cheapSQL, requests)
	expAllowed, expThrottled := countAllowed(t, ts, "exp-key", expensiveSQL, requests)

	t.Logf("cheap: %d allowed / %d throttled; expensive: %d allowed / %d throttled",
		cheapAllowed, cheapThrottled, expAllowed, expThrottled)

	assert.Greater(t, cheapAllowed, expAllowed,
		"the cheap-query tenant should be admitted more often than the expensive-query tenant")
	assert.GreaterOrEqual(t, expAllowed, 1, "an expensive query should fit in the burst at least once")
	assert.Greater(t, cheapThrottled, 0, "throttling should actually kick in within the request budget")
	assert.Greater(t, expThrottled, 0)
}

// TestThrottle_429Details checks the rejection contract: a query whose cost
// exceeds the whole burst can never be admitted, and the 429 carries a
// Retry-After header plus an explanatory body.
func TestThrottle_429Details(t *testing.T) {
	cfg := Config{
		DataDir:      "../testdata",
		DefaultQuota: Quota{Rate: 1, Burst: 5}, // tiny bucket; an expensive query won't fit
		CostPerToken: 1,
		APIKeys:      map[string]KeyConfig{"k": {Tenant: "t"}},
	}
	ts := serverWith(t, cfg)

	body := `{"sql":"SELECT o.name, COUNT(*) FROM orders o JOIN customers c ON o.name = c.name GROUP BY o.name"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/query", bytes.NewBufferString(body))
	req.Header.Set("X-API-Key", "k")
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.NotEmpty(t, resp.Header.Get("Retry-After"))

	raw, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, "rate limit exceeded", decoded["error"])
	assert.Greater(t, decoded["estimated_cost"].(float64), 0.0)
	assert.Greater(t, decoded["tokens_required"].(float64), 5.0, "cost exceeds the burst of 5")
	assert.Contains(t, decoded, "retry_after_seconds")
}

// TestThrottle_CheapQueryAdmittedRepeatedly confirms a cheap query passes many
// times before exhausting a generous quota.
func TestThrottle_CheapQueryAdmittedRepeatedly(t *testing.T) {
	cfg := Config{
		DataDir:      "../testdata",
		DefaultQuota: Quota{Rate: 0, Burst: 1000},
		CostPerToken: 100, // make cheap queries cost ~1 token
		APIKeys:      map[string]KeyConfig{"k": {Tenant: "t"}},
	}
	ts := serverWith(t, cfg)
	allowed, throttled := countAllowed(t, ts, "k", "SELECT id FROM orders LIMIT 1", 20)
	assert.Equal(t, 20, allowed, "cheap queries stay well within a generous quota")
	assert.Equal(t, 0, throttled)
}
