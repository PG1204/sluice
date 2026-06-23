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

// testConfig points at the repo-root sample data (api/ is one level down) and
// defines a default-quota key and a higher-quota "vip" key.
func testConfig() Config {
	return Config{
		DataDir:      "../testdata",
		DefaultQuota: Quota{Rate: 10, Burst: 100},
		APIKeys: map[string]KeyConfig{
			"dev-key": {Tenant: "dev"},
			"vip-key": {Tenant: "vip", Quota: &Quota{Rate: 50, Burst: 500}},
		},
	}
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(NewServer(testConfig(), nil).Handler())
	t.Cleanup(ts.Close)
	return ts
}

// do issues a request and returns the status code and decoded JSON body.
func do(t *testing.T, ts *httptest.Server, method, path, key, body string) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	require.NoError(t, err)
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var decoded map[string]any
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) > 0 {
		require.NoError(t, json.Unmarshal(raw, &decoded), "body: %s", raw)
	}
	return resp.StatusCode, decoded
}

func TestHealth_NoAuth(t *testing.T) {
	ts := newTestServer(t)
	status, body := do(t, ts, http.MethodGet, "/health", "", "")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "ok", body["status"])
}

func TestAuth_Required(t *testing.T) {
	ts := newTestServer(t)

	status, _ := do(t, ts, http.MethodPost, "/query", "", `{"sql":"SELECT name FROM orders"}`)
	assert.Equal(t, http.StatusUnauthorized, status, "no key")

	status, _ = do(t, ts, http.MethodPost, "/query", "wrong-key", `{"sql":"SELECT name FROM orders"}`)
	assert.Equal(t, http.StatusUnauthorized, status, "bad key")
}

func TestQuery_Success(t *testing.T) {
	ts := newTestServer(t)
	status, body := do(t, ts, http.MethodPost, "/query", "dev-key",
		`{"sql":"SELECT name, COUNT(*) FROM orders WHERE amount > 100 GROUP BY name"}`)

	require.Equal(t, http.StatusOK, status)
	assert.EqualValues(t, 3, body["row_count"])
	cols := body["columns"].([]any)
	assert.Len(t, cols, 2)
	rows := body["rows"].([]any)
	assert.Len(t, rows, 3)
}

func TestQuery_Errors(t *testing.T) {
	ts := newTestServer(t)
	tests := []struct {
		name string
		body string
		want int
	}{
		{"bad SQL", `{"sql":"SELECT FROM"}`, http.StatusBadRequest},
		{"unknown table", `{"sql":"SELECT x FROM nope"}`, http.StatusBadRequest},
		{"empty sql", `{"sql":""}`, http.StatusBadRequest},
		{"malformed JSON", `{"sql":`, http.StatusBadRequest},
		{"unknown field", `{"query":"SELECT 1"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := do(t, ts, http.MethodPost, "/query", "dev-key", tt.body)
			assert.Equal(t, tt.want, status)
			assert.NotEmpty(t, body["error"])
		})
	}
}

func TestExplain_ReturnsPlanAndCost(t *testing.T) {
	ts := newTestServer(t)
	status, body := do(t, ts, http.MethodPost, "/explain", "dev-key",
		`{"sql":"SELECT name FROM orders WHERE amount > 100"}`)

	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, body["plan"], "Total cost:")
	assert.Greater(t, body["cost"].(float64), 0.0)
}

func TestTables(t *testing.T) {
	ts := newTestServer(t)
	status, body := do(t, ts, http.MethodGet, "/tables", "dev-key", "")

	require.Equal(t, http.StatusOK, status)
	tables := body["tables"].([]any)
	assert.Len(t, tables, 2) // customers, orders
	first := tables[0].(map[string]any)
	assert.NotEmpty(t, first["name"])
	assert.NotEmpty(t, first["columns"])
}

func TestQuota_ReportsConfiguredLimitsWithoutConsuming(t *testing.T) {
	ts := newTestServer(t)

	status, body := do(t, ts, http.MethodGet, "/quota", "dev-key", "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "dev", body["tenant"])
	assert.EqualValues(t, 100, body["burst"])
	assert.EqualValues(t, 100, body["remaining"], "peek must not consume tokens")

	// A second peek still shows full quota.
	_, body2 := do(t, ts, http.MethodGet, "/quota", "dev-key", "")
	assert.EqualValues(t, 100, body2["remaining"])

	// The vip key has its own, larger quota.
	_, vip := do(t, ts, http.MethodGet, "/quota", "vip-key", "")
	assert.Equal(t, "vip", vip["tenant"])
	assert.EqualValues(t, 500, vip["burst"])
}
